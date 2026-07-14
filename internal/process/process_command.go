package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/event"
	"github.com/androidand/llama-skein/internal/logmon"
	"github.com/androidand/llama-skein/internal/shared"
)

var ErrStartAborted = fmt.Errorf("aborted")

// cmdWaitDelay is the upper bound the runtime will wait for child I/O to
// drain after the process exits before force-closing the stdout/stderr
// pipes. Required so that cmd.Wait() returns even when a forked grandchild
// inherits and holds the pipes open (e.g. a shell wrapper that backgrounds
// the real binary). killProcess sends the stop signal directly (not via the
// cmd context), so this delay is measured from process exit rather than from
// the stop request, and stays independent of the caller's graceful timeout.
const cmdWaitDelay = 10 * time.Second

// parentCancelGraceTimeout is the graceful timeout used when the process is
// torn down because parentCtx was cancelled (final router teardown or app
// shutdown). In the normal flow the process has already been stopped via
// Stop() by this point, so killProcess is a no-op kill; the short grace just
// bounds the rare case where a process is still alive when its context is cut.
const parentCancelGraceTimeout = time.Second

// Crash-loop breaker: when the upstream exits unexpectedly (not via Stop)
// crashLoopThreshold times within crashLoopWindow, restarts are refused until
// crashLoopCooldown has elapsed since the most recent exit. This surfaces a
// clear error to clients instead of silently cold-starting a crashing backend
// on every request. An explicit Stop (manual unload, TTL) resets the history.
const (
	crashLoopWindow    = 10 * time.Minute
	crashLoopThreshold = 3
	crashLoopCooldown  = time.Minute
)

// Inference probe: mlx_lm.server runs generation on a single background
// thread that can die (e.g. after a client disconnect mid-stream) while its
// HTTP server keeps answering /health with 200, leaving the model "ready" but
// unable to serve completions. While a backend:mlx model is ready and idle, a
// 1-token completion is sent every inferenceProbeInterval; after
// inferenceProbeThreshold consecutive failures the process is stopped so the
// next request restarts it cleanly instead of hanging forever.
const (
	inferenceProbeInterval  = time.Minute
	inferenceProbeTimeout   = time.Minute
	inferenceProbeThreshold = 2
)

type runReq struct {
	timeout time.Duration
	respond chan error
}

type stopReq struct {
	timeout time.Duration
	respond chan error
}

type waitReadyReq struct {
	respond chan error
}

type startResult struct {
	cmd       *exec.Cmd
	cmdDone   chan struct{}
	cancel    context.CancelFunc
	handlerFn http.HandlerFunc
	err       error
}

type ProcessCommand struct {
	id        string
	config    config.ModelConfig
	parentCtx context.Context

	processLogger *logmon.Monitor
	proxyLogger   *logmon.Monitor

	// waitDelay is assigned to cmd.WaitDelay when starting the upstream
	// process. Defaults to cmdWaitDelay; tests override it to keep the
	// pipe-close backstop from dominating their runtime.
	waitDelay time.Duration

	// probe cadence for inferenceProbeLoop. Defaults to the inferenceProbe*
	// constants; tests override them.
	probeInterval time.Duration
	probeTimeout  time.Duration

	// skipPortReclaim disables the pre-start stale-orphan port reclaim. Set by
	// tests, where the proxy target is a separate mock server on the proxy port
	// (in production the model's own upstream binds it), so reclaiming would
	// kill the test's mock.
	skipPortReclaim bool

	runCh       chan runReq
	stopCh      chan stopReq
	waitReadyCh chan waitReadyReq

	// current ProcessState. Written only by run(); read by State() via atomic load.
	state atomic.Value

	// stores the active reverse-proxy handler when the process is running.
	// Written only by run(); read by ServeHTTP via atomic load.
	handler atomic.Pointer[http.HandlerFunc]

	lastUse  atomic.Int64 // unix nano timestamp of last ServeHTTP completion
	inflight atomic.Int64 // current in-flight ServeHTTP calls

	// serializeSlot bounds concurrent inference to the backend's real slot
	// count so requests never race into a slot the backend can't serve
	// concurrently. For MLX it is capacity 1 — mlx_lm.server's
	// ThreadingHTTPServer crashes outright when two requests arrive at once
	// (observed 0.31.3: both connections EOF and the process exits). For
	// llama.cpp it is the explicit --parallel/-np value, so two requests can't
	// deadlock a --parallel 1 server's single GPU slot. Requests QUEUE here
	// rather than being rejected, so a slow load or a long generation doesn't
	// turn into a 429. The inference probe acquires the same slot so it can
	// never overlap a real request. Nil = unbounded (llama.cpp without an
	// explicit --parallel: slot count is version-dependent, so we don't assume).
	serializeSlot chan struct{}

	// crashMu guards crashTimes: timestamps of unexpected upstream exits
	// within crashLoopWindow. Appended by run(), read by doStart, cleared
	// by an explicit Stop.
	crashMu    sync.Mutex
	crashTimes []time.Time
}

var _ Process = (*ProcessCommand)(nil)

func New(
	parentCtx context.Context,
	id string,
	conf config.ModelConfig,
	processLogger *logmon.Monitor,
	proxyLogger *logmon.Monitor,
) (*ProcessCommand, error) {
	p := &ProcessCommand{
		id:            id,
		config:        conf,
		parentCtx:     parentCtx,
		processLogger: processLogger,
		proxyLogger:   proxyLogger,

		runCh:         make(chan runReq),
		stopCh:        make(chan stopReq),
		waitReadyCh:   make(chan waitReadyReq),
		waitDelay:     cmdWaitDelay,
		probeInterval: inferenceProbeInterval,
		probeTimeout:  inferenceProbeTimeout,
	}
	if conf.Backend == config.BackendMLX {
		p.serializeSlot = make(chan struct{}, 1)
	} else if conf.IsLlamaCpp() {
		// Bound llama.cpp to its explicit slot count so concurrent requests
		// can't deadlock a --parallel 1 GPU slot. No explicit flag → unbounded
		// (implicit slot count is version-dependent; don't assume).
		if n, ok := parallelFromCmd(conf.Cmd); ok {
			p.serializeSlot = make(chan struct{}, n)
		}
	}
	p.state.Store(StateStopped)

	go p.run()
	return p, nil
}

func (p *ProcessCommand) Logger() *logmon.Monitor { return p.processLogger }

// parallelFromCmd extracts the llama.cpp slot count from a launch command's
// --parallel / -np flag (bare "flag N" or "flag=N"). ok is false when the flag
// is absent or unparseable, so the caller keeps its default (unbounded) rather
// than assuming llama.cpp's version-dependent implicit slot count.
func parallelFromCmd(cmd string) (int, bool) {
	tokens := strings.Fields(cmd)
	for i, tok := range tokens {
		var val string
		switch {
		case tok == "--parallel" || tok == "-np":
			if i+1 < len(tokens) {
				val = tokens[i+1]
			}
		case strings.HasPrefix(tok, "--parallel="):
			val = strings.TrimPrefix(tok, "--parallel=")
		case strings.HasPrefix(tok, "-np="):
			val = strings.TrimPrefix(tok, "-np=")
		}
		if val != "" {
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				return n, true
			}
		}
	}
	return 0, false
}

// run is the single-writer goroutine that owns all mutable lifecycle state
// (current ProcessState, the running *exec.Cmd, the active reverse-proxy
// handler, and the list of WaitReady subscribers). Every public method
// (Run / Stop / State / WaitReady) is a thin client that sends a request on
// one of the channels below and waits for a response — this funnels concurrent
// callers through a single serialization point so the state machine never
// observes a race.
func (p *ProcessCommand) run() {
	// Mutable state — only read/written from this goroutine. ServeHTTP reads
	// p.handler concurrently, which is why handler is an atomic.Pointer.
	// p.state mirrors `state` so State() can observe transitions; setState
	// writes both.
	state := StateStopped
	setState := func(s ProcessState) {
		old := state
		state = s
		p.state.Store(s)
		if old != s {
			event.Emit(shared.ProcessStateChangeEvent{
				ProcessName: p.id,
				OldState:    string(old),
				NewState:    string(s),
			})
		}
	}
	var (
		cmd          *exec.Cmd
		cmdDone      <-chan struct{}
		cmdCancel    context.CancelFunc
		readyWaiters []waitReadyReq
		// runResp parks the in-flight Run caller's response channel. The
		// interface contract is that Run blocks until the process is
		// terminated, so we hold this until Stop, parentCtx, or an
		// upstream exit unblocks it via respondRun.
		runResp chan<- error
	)

	// notifyWaiters wakes every blocked WaitReady caller with the given result.
	// Used on transitions out of StateStarting (ready, failed, aborted, or
	// shutdown) — anything that resolves the "is it ready yet?" question.
	notifyWaiters := func(err error) {
		for _, w := range readyWaiters {
			select {
			case w.respond <- err:
			default:
			}
		}
		readyWaiters = nil
	}

	// respondRun delivers the final Run result, if a Run caller is parked.
	respondRun := func(err error) {
		if runResp != nil {
			runResp <- err
			runResp = nil
		}
	}

	for {
		select {
		// Shutdown: parent context cancelled. Tear down any running process,
		// wake any pending WaitReady callers with an error, then exit the
		// goroutine permanently. Subsequent public-method calls will fail
		// because parentCtx.Done() unblocks their send-side selects.
		case <-p.parentCtx.Done():
			// Mark shutdown before killProcess so concurrent State() readers
			// stop treating this process as ready while the (possibly slow)
			// teardown is in progress.
			setState(StateShutdown)
			if cmd != nil {
				p.handler.Store(nil)
				p.killProcess(cmd, cmdCancel, cmdDone, parentCancelGraceTimeout)
				cmd = nil
				cmdDone = nil
				cmdCancel = nil
			}
			notifyWaiters(fmt.Errorf("[%s] shutdown", p.id))
			respondRun(fmt.Errorf("[%s] shutdown", p.id))
			return

		// Upstream exited on its own (not via Stop). Drop handler state,
		// transition to Stopped, and unblock the parked Run caller.
		// cmdDone is nil while no process is running, so this case is
		// dormant outside of StateReady.
		case <-cmdDone:
			if cmdCancel != nil {
				cmdCancel()
			}
			cmd = nil
			cmdDone = nil
			cmdCancel = nil
			p.handler.Store(nil)
			setState(StateStopped)
			crashes := p.recordUnexpectedExit()
			p.proxyLogger.Warnf("<%s> upstream exited unexpectedly (%d unexpected exit(s) in the last %v)", p.id, crashes, crashLoopWindow)
			respondRun(fmt.Errorf("[%s] upstream exited unexpectedly", p.id))

		// WaitReady: if we're already in a terminal-for-this-question state,
		// respond immediately; otherwise queue the caller and let a future
		// state transition wake them via notifyWaiters.
		case req := <-p.waitReadyCh:
			switch state {
			case StateReady:
				req.respond <- nil
			case StateShutdown:
				req.respond <- fmt.Errorf("[%s] shutdown", p.id)
			default:
				readyWaiters = append(readyWaiters, req)
			}

		// Run: start the upstream process. Only valid from StateStopped.
		// doStart can take a long time (health-check polling), so it runs in
		// a separate goroutine and we wait on resultCh. While waiting we also
		// listen for an incoming Stop — that's how callers cancel an in-flight
		// start.
		case req := <-p.runCh:
			if state != StateStopped {
				req.respond <- fmt.Errorf("[%s] could not be started in %s state", p.id, state)
				continue
			}
			setState(StateStarting)

			startCtx, cancelStart := context.WithCancel(context.Background())
			resultCh := make(chan startResult, 1)
			go func() {
				resultCh <- p.doStart(startCtx, req.timeout)
			}()

			// pendingStop holds a Stop request that arrived mid-start, so we
			// can respond to it AFTER we've finished tearing the start down.
			var pendingStop *stopReq
			select {
			// doStart finished on its own — either successfully (latch
			// cmd/handler and move to Ready) or with an error (back to
			// Stopped). Either way wake WaitReady subscribers and reply
			// to the Run caller.
			case res := <-resultCh:
				if res.err == nil {
					cmd = res.cmd
					cmdDone = res.cmdDone
					cmdCancel = res.cancel
					fn := res.handlerFn
					p.handler.Store(&fn)
					setState(StateReady)
					notifyWaiters(nil)
					// Park the Run response — Run blocks until the process
					// terminates, so we only fire this when Stop, parentCtx,
					// or the upstream exit takes the process down.
					runResp = req.respond

					// MLX's HTTP server keeps answering /health after its
					// generation thread dies; probe with real inference so a
					// wedged backend is restarted instead of hanging callers.
					// Self-terminates when state leaves StateReady.
					if p.config.Backend == config.BackendMLX {
						go p.inferenceProbeLoop()
					}

					// Start TTL goroutine if configured — self-terminates
					// when state leaves StateReady.
					if p.config.UnloadAfter > 0 {
						ttlDuration := time.Duration(p.config.UnloadAfter) * time.Second
						go func() {
							ticker := time.NewTicker(time.Second)
							defer ticker.Stop()
							for range ticker.C {
								if p.State() != StateReady {
									return
								}
								if p.inflight.Load() != 0 {
									continue
								}
								if time.Since(time.Unix(0, p.lastUse.Load())) > ttlDuration {
									p.proxyLogger.Infof("<%s> Unloading model, TTL of %ds reached", p.id, p.config.UnloadAfter)
									p.Stop(10 * time.Second)
									return
								}
							}
						}()
					}
				} else {
					setState(StateStopped)
					notifyWaiters(res.err)
					req.respond <- res.err
				}

			// Stop arrived while doStart was still running. Cancel the
			// start context to abort it, then wait for doStart to return.
			// If doStart had already crossed the finish line before
			// cancellation took effect, it returns a live cmd that we
			// must kill ourselves. The Run caller gets ErrAbort; the Stop
			// caller is parked in pendingStop and answered below.
			case stop := <-p.stopCh:
				cancelStart()
				res := <-resultCh
				if res.cmd != nil {
					p.killProcess(res.cmd, res.cancel, res.cmdDone, stop.timeout)
				}
				setState(StateStopped)
				notifyWaiters(ErrStartAborted)
				req.respond <- ErrStartAborted
				pendingStop = &stop

			// Parent context cancelled (e.g. config reload) while doStart
			// was still running. Stop() returns early when parentCtx is
			// done and never sends on stopCh, so we must handle shutdown
			// here to avoid leaving doStart running indefinitely.
			case <-p.parentCtx.Done():
				cancelStart()
				// Mark shutdown before tearing the process down: killProcess
				// may block (e.g. taskkill on Windows is slow to spawn), and
				// callers observing State() should see StateShutdown promptly
				// rather than a stale StateStarting.
				setState(StateShutdown)
				res := <-resultCh
				if res.cmd != nil {
					p.killProcess(res.cmd, res.cancel, res.cmdDone, parentCancelGraceTimeout)
				}
				notifyWaiters(fmt.Errorf("[%s] shutdown", p.id))
				respondRun(fmt.Errorf("[%s] shutdown", p.id))
				return
			}
			// cancelStart is idempotent; calling it again here ensures the
			// context is released even on the success path (govet leak check).
			cancelStart()
			if pendingStop != nil {
				pendingStop.respond <- nil
			}

		// Stop: tear down a running process.
		case stop := <-p.stopCh:
			if cmd != nil {
				setState(StateStopping)
				p.killProcess(cmd, cmdCancel, cmdDone, stop.timeout)
				cmd = nil
				cmdDone = nil
				cmdCancel = nil
				p.handler.Store(nil)
			}
			// Stop is a no-op (and not an error) when already Stopped — this
			// is what makes it idempotent for callers that don't track state.
			setState(StateStopped)
			// An explicit Stop (manual unload, TTL, shutdown path) is the
			// operator's reset lever for the crash-loop breaker.
			p.clearCrashHistory()
			respondRun(nil)
			stop.respond <- nil
		}
	}
}

func (p *ProcessCommand) doStart(startCtx context.Context, healthCheckTimeout time.Duration) startResult {
	if err := p.crashLoopError(); err != nil {
		// Don't fail faster than the router's WaitReady call can register:
		// baseRouter.doSwap issues WaitReady right after Run, and an instant
		// failure here could resolve the whole Run before that waiter lands,
		// stranding it. The 250ms mirrors the post-start grace below.
		select {
		case <-startCtx.Done():
		case <-time.After(250 * time.Millisecond):
		}
		return startResult{err: err}
	}

	if p.config.Proxy == "" {
		return startResult{err: fmt.Errorf("upstream proxy missing")}
	}

	args, err := p.config.SanitizedCommand()
	if err != nil {
		return startResult{err: fmt.Errorf("unable to get sanitized command: %w", err)}
	}

	proxyURL, err := url.Parse(p.config.Proxy)
	if err != nil {
		return startResult{err: fmt.Errorf("invalid proxy URL %q: %w", p.config.Proxy, err)}
	}

	reverseProxy := httputil.NewSingleHostReverseProxy(proxyURL)
	reverseProxy.Transport = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   time.Duration(p.config.Timeouts.Connect) * time.Second,
			KeepAlive: time.Duration(p.config.Timeouts.KeepAlive) * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   time.Duration(p.config.Timeouts.TLSHandshake) * time.Second,
		ResponseHeaderTimeout: time.Duration(p.config.Timeouts.ResponseHeader) * time.Second,
		ExpectContinueTimeout: time.Duration(p.config.Timeouts.ExpectContinue) * time.Second,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       time.Duration(p.config.Timeouts.IdleConn) * time.Second,
	}
	reverseProxy.ModifyResponse = func(resp *http.Response) error {
		if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
			resp.Header.Set("X-Accel-Buffering", "no")
		}
		// Remap llama.cpp context-overflow errors to HTTP 413 so callers
		// (skein, opencode) can detect overflow without body string matching.
		if p.config.IsLlamaCpp() && resp.StatusCode == http.StatusBadRequest {
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(body)) // always restore
			if err == nil {
				var errBody struct {
					Error struct {
						Type string `json:"type"`
					} `json:"error"`
				}
				if json.Unmarshal(body, &errBody) == nil &&
					errBody.Error.Type == "exceed_context_size_error" {
					resp.StatusCode = http.StatusRequestEntityTooLarge // 413
				}
			}
		}
		return nil
	}
	// ErrorHandler fires when the upstream round-trip fails or the response
	// body copy breaks — i.e. the backend process became unreachable mid
	// request. The common cause on this fork is a backend that aborted while
	// serving: mlx_lm in particular has no graceful out-of-memory path and
	// SIGABRTs when a request's context exceeds the Metal allocation budget,
	// so the connection is reset. Without a handler net/http returns a bare
	// 502 with no detail; instead emit a clean, structured, retryable error
	// so callers (opencode, skein) can surface a useful message and retry.
	//
	// Recovery itself is handled elsewhere: when the process exits, cmdDone
	// fires and run() clears the handler + sets StateStopped, so the *next*
	// request reloads with loading-state. We deliberately do NOT Stop() the
	// process from here — an earlier self-heal ErrorHandler that did so caused
	// an endless reload loop when a healthy IPv6/IPv4-mismatched upstream
	// looked unreachable (since fixed by pinning the proxy to 127.0.0.1).
	reverseProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// Client disconnected: nothing to send, and writing races the torn-down
		// connection. The mid-stream ErrAbortHandler case is recovered below.
		if r.Context().Err() != nil {
			return
		}
		p.proxyLogger.Warnf("<%s> upstream unreachable while serving request: %v", p.id, err)

		msg := fmt.Sprintf("model backend %q became unreachable while handling this request (%v). It is being restarted — retry shortly.", p.id, err)
		if !p.config.IsLlamaCpp() {
			// mlx/vllm: most often an out-of-memory abort for the request size.
			msg = fmt.Sprintf("model backend %q exited while handling this request — most often an out-of-memory abort for the request's context size. It is being restarted; retry shortly, ideally with a smaller prompt/context.", p.id)
		}
		body, _ := json.Marshal(struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
			} `json:"error"`
		}{Error: struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		}{Message: msg, Type: "upstream_unavailable", Code: "backend_exited"}})

		// If the upstream died mid-stream the headers are already sent and
		// WriteHeader is a no-op; the JSON is appended as a terminal error
		// frame, which still beats a silently truncated stream.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write(body)
	}
	// httputil.ReverseProxy panics with http.ErrAbortHandler when the upstream
	// disconnects after response headers have been sent. Recover here so the
	// streaming termination is treated as a normal client/upstream disconnect.
	// see: https://github.com/golang/go/issues/23643
	handlerFn := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					p.proxyLogger.Infof("<%s> recovered from upstream disconnection during streaming", p.id)
				} else {
					p.proxyLogger.Warnf("<%s> recovered from panic: %v", p.id, rec)
				}
			}
		}()
		reverseProxy.ServeHTTP(w, r)
	})

	// cmdCtx + cmd.Cancel are wired as a safety net: if the context is ever
	// cancelled while the process is alive, cmd.Cancel sends SIGTERM / CmdStop
	// and the runtime escalates to SIGKILL after cmd.WaitDelay. In the normal
	// teardown path killProcess sends the stop signal directly instead, so
	// cmd.WaitDelay only acts as the inherited-pipe backstop measured from
	// process exit (see killProcess).
	cmdCtx, cmdCancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(cmdCtx, args[0], args[1:]...)
	cmd.Stderr = p.processLogger
	cmd.Stdout = p.processLogger
	cmd.Env = append(cmd.Environ(), p.config.Env...)
	cmd.Cancel = func() error { return p.sendStopSignal(cmd) }
	cmd.WaitDelay = p.waitDelay
	setProcAttributes(cmd)

	// Reclaim the model's assigned port from a stale orphan before starting.
	// If a previous upstream survived (llama-skein was SIGKILLed — crash, OOM,
	// or `launchctl kickstart -k` — so it could not reap its child), it still
	// holds this port. The new process would fail to bind ("exited prematurely")
	// while the reverse proxy forwards to the orphan's garbage. The port is ours
	// (assigned from startPort) and localhost-only, so reclaiming it is safe.
	if !p.skipPortReclaim {
		if n := reclaimStalePort(proxyURL.Host); n > 0 {
			p.proxyLogger.Warnf("<%s> reclaimed assigned port %s from %d stale process(es) before start", p.id, proxyURL.Host, n)
		}
	}

	p.proxyLogger.Debugf("<%s> Executing start command: %s, env: %s", p.id, strings.Join(args, " "), strings.Join(p.config.Env, ", "))

	cmdDone := make(chan struct{})
	if err := cmd.Start(); err != nil {
		cmdCancel()
		return startResult{err: fmt.Errorf("failed to start command '%s': %w", strings.Join(args, " "), err)}
	}

	go func() {
		waitErr := cmd.Wait()
		switch st := p.State(); {
		case waitErr == nil:
			p.proxyLogger.Debugf("<%s> process exited cleanly", p.id)
		case st == StateStopping || st == StateShutdown:
			// Expected: we force-terminated the process. A forced kill exits
			// the child with a non-zero code (e.g. taskkill /f on Windows
			// yields exit status 1), so this is not an error.
			p.proxyLogger.Debugf("<%s> process stopped by llama-skein: %v", p.id, waitErr)
		default:
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				p.proxyLogger.Debugf("<%s> process exited: code=%d, err=%v", p.id, exitErr.ExitCode(), waitErr)
			} else {
				p.proxyLogger.Debugf("<%s> process exited with error: %v", p.id, waitErr)
			}
		}
		close(cmdDone)
	}()

	abort := func(err error) startResult {
		p.killProcess(cmd, cmdCancel, cmdDone, 5*time.Second)
		return startResult{err: err}
	}
	prematureExit := func() startResult {
		cmdCancel()
		return startResult{err: fmt.Errorf("upstream command exited prematurely")}
	}

	if startCtx.Err() != nil {
		return abort(ErrStartAborted)
	}

	checkEndpoint := strings.TrimSpace(p.config.CheckEndpoint)
	if checkEndpoint == "none" {
		return startResult{cmd: cmd, cmdDone: cmdDone, cancel: cmdCancel, handlerFn: handlerFn}
	}

	// Wait 250ms for the command to start up before health checking
	select {
	case <-startCtx.Done():
		return abort(ErrStartAborted)
	case <-time.After(250 * time.Millisecond):
	}

	deadline := time.Now().Add(healthCheckTimeout)
	for {
		select {
		case <-startCtx.Done():
			return abort(ErrStartAborted)
		case <-cmdDone:
			return prematureExit()
		default:
		}

		if time.Now().After(deadline) {
			return abort(fmt.Errorf("health check timed out after %v", healthCheckTimeout))
		}

		req, _ := http.NewRequestWithContext(startCtx, "GET", p.config.CheckEndpoint, nil)
		rr := httptest.NewRecorder()
		reverseProxy.ServeHTTP(rr, req)
		resp := rr.Result()
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			p.proxyLogger.Infof("<%s> Health check passed on %s%s", p.id, p.config.Proxy, p.config.CheckEndpoint)
			break
		} else if startCtx.Err() != nil {
			return abort(ErrStartAborted)
		}

		select {
		case <-startCtx.Done():
			return abort(ErrStartAborted)
		case <-cmdDone:
			return prematureExit()
		case <-time.After(time.Second):
		}
	}

	// MLX and vLLM answer /health 200 before the model is resident, so /health
	// alone cannot mean "ready". A warm-up inference forces eager loading AND
	// proves the model is actually serving. It GATES readiness: if warm-up
	// fails, the process is up but not resident — abort rather than mark the
	// model ready, so /running/v1/models never report a model that can't serve
	// (which would make the next request cold-load for ~15s or hang). The start
	// error reaches the caller; repeated failures trip the crash-loop breaker.
	if !p.config.IsLlamaCpp() {
		p.proxyLogger.Infof("<%s> Warming up model (verifying residency for %s backend)", p.id, p.config.Backend)
		warmupCtx, warmupCancel := context.WithTimeout(startCtx, 10*time.Minute)
		defer warmupCancel()
		if err := p.warmupModel(warmupCtx); err != nil {
			if startCtx.Err() != nil {
				return abort(ErrStartAborted) // stop/shutdown cancelled the warm-up
			}
			// A model that comes up but can't serve is a crash for breaker
			// purposes: count it so repeated failures surface the explicit
			// "refusing restart" error instead of a start+fail cycle per request.
			crashes := p.recordUnexpectedExit()
			p.proxyLogger.Warnf("<%s> verified-readiness warm-up failed (%d failure(s) in the last %v): %v", p.id, crashes, crashLoopWindow, err)
			return abort(fmt.Errorf("model warm-up failed (process up but not serving): %w", err))
		}
		p.proxyLogger.Infof("<%s> Model warm-up complete (verified resident)", p.id)
	}

	return startResult{cmd: cmd, cmdDone: cmdDone, cancel: cmdCancel, handlerFn: handlerFn}
}

// sendStopSignal runs the configured CmdStop (if any) or sends SIGTERM to
// the upstream process. Wired up as cmd.Cancel so it fires whenever the
// cmd's context is cancelled.
func (p *ProcessCommand) sendStopSignal(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		p.processLogger.Debugf("<%s> sendStopSignal() called with nil cmd or process, nothing to stop", p.id)
		return nil
	}
	pid := cmd.Process.Pid
	if p.config.CmdStop != "" {
		p.processLogger.Debugf("<%s> sendStopSignal() using CmdStop %q for pid %d", p.id, p.config.CmdStop, pid)
		stopArgs, err := config.SanitizeCommand(
			strings.ReplaceAll(p.config.CmdStop, "${PID}", fmt.Sprintf("%d", pid)),
		)
		if err == nil {
			p.processLogger.Debugf("<%s> sendStopSignal() running stop command: %s", p.id, strings.Join(stopArgs, " "))
			stopCmd := exec.Command(stopArgs[0], stopArgs[1:]...)
			stopCmd.Env = cmd.Env
			setProcAttributes(stopCmd)
			runErr := stopCmd.Run()
			if runErr != nil {
				p.processLogger.Errorf("<%s> sendStopSignal() stop command failed: %v", p.id, runErr)
			} else {
				p.processLogger.Debugf("<%s> sendStopSignal() stop command completed for pid %d", p.id, pid)
			}
			return runErr
		}
		// fall through to SIGTERM if sanitize failed
		p.processLogger.Errorf("<%s> sendStopSignal() failed to sanitize CmdStop %q: %v, falling back to terminateProcessTree", p.id, p.config.CmdStop, err)
	}
	// On Unix this SIGTERMs the whole process group so a forked grandchild
	// (e.g. a shell wrapper that backgrounds the real binary) is taken down
	// with the parent rather than orphaned.
	p.processLogger.Debugf("<%s> sendStopSignal() no CmdStop configured, calling terminateProcessTree for pid %d", p.id, pid)
	termErr := terminateProcessTree(cmd)
	if termErr != nil {
		p.processLogger.Errorf("<%s> sendStopSignal() terminateProcessTree failed for pid %d: %v", p.id, pid, termErr)
	}
	return termErr
}

// killProcess terminates the upstream process. The flow:
//
//  1. Send the graceful stop signal (CmdStop / SIGTERM) directly — NOT by
//     cancelling cmdCtx. Cancelling the context would start cmd.WaitDelay
//     immediately, which force-kills the process WaitDelay after the signal
//     and would silently cap gracefulTimeout at WaitDelay whenever
//     gracefulTimeout is the longer of the two.
//  2. We wait up to gracefulTimeout for the process to exit on its own.
//  3. If still alive, we SIGKILL the process group directly (Unix) so any
//     forked descendant is force-terminated alongside the parent.
//  4. We wait on cmdDone. cmd.WaitDelay (set when the cmd was built) is the
//     critical backstop here: once the process exits, if a forked grandchild
//     inherited the stdout/stderr pipes and is still holding them, the runtime
//     force-closes the pipes WaitDelay after the exit and cmd.Wait() unblocks.
//     Because we never cancelled the context, that WaitDelay timer measures
//     from process exit (see os/exec awaitGoroutines), not from this call.
//     Without WaitDelay this select would hang forever (the v219 bug).
//
// cancel() is still invoked (deferred) to release the context, but only after
// the process has exited and os/exec's ctx watcher has already torn down, so it
// never re-fires cmd.Cancel.
func (p *ProcessCommand) killProcess(cmd *exec.Cmd, cancel context.CancelFunc, cmdDone <-chan struct{}, gracefulTimeout time.Duration) {
	if cancel == nil {
		return
	}
	defer cancel()

	// Deliver CmdStop / SIGTERM in a goroutine so a slow or hanging CmdStop
	// cannot block the run() goroutine; the gracefulTimeout + Process.Kill
	// path below still guarantees teardown.
	if cmd != nil {
		go func() { _ = p.sendStopSignal(cmd) }()
	}

	timer := time.NewTimer(gracefulTimeout)
	defer timer.Stop()

	select {
	case <-cmdDone:
		return
	case <-timer.C:
	}

	if cmd != nil {
		// SIGKILL the whole process group on Unix so any descendant that
		// ignored or outlived the graceful signal is force-terminated too.
		_ = killProcessTree(cmd)
	}
	<-cmdDone
}

func (p *ProcessCommand) ID() string {
	return p.id
}

func (p *ProcessCommand) Run(timeout time.Duration) error {
	req := runReq{
		timeout: timeout,
		respond: make(chan error, 1),
	}
	select {
	case p.runCh <- req:
	case <-p.parentCtx.Done():
		return fmt.Errorf("[%s] shutdown", p.id)
	}
	select {
	case err := <-req.respond:
		return err
	case <-p.parentCtx.Done():
		return fmt.Errorf("[%s] shutdown", p.id)
	}
}

func (p *ProcessCommand) WaitReady(ctx context.Context) error {
	req := waitReadyReq{respond: make(chan error, 1)}
	select {
	case p.waitReadyCh <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-p.parentCtx.Done():
		return fmt.Errorf("[%s] shutdown", p.id)
	}
	select {
	case err := <-req.respond:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *ProcessCommand) Stop(timeout time.Duration) error {
	req := stopReq{
		timeout: timeout,
		respond: make(chan error, 1),
	}
	select {
	case p.stopCh <- req:
	case <-p.parentCtx.Done():
		return fmt.Errorf("[%s] shutdown", p.id)
	}
	return <-req.respond
}

func (p *ProcessCommand) State() ProcessState {
	if s, ok := p.state.Load().(ProcessState); ok {
		return s
	}
	return StateStopped
}

func (p *ProcessCommand) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fn := p.handler.Load()
	if fn == nil {
		http.Error(w, fmt.Sprintf("llama-skein-error: [%s] process is not ready", p.id), http.StatusServiceUnavailable)
		return
	}
	p.inflight.Add(1)
	defer func() {
		p.lastUse.Store(time.Now().UnixNano())
		p.inflight.Add(-1)
	}()
	// Serialize to the backend's slot count: queue (honouring client
	// disconnect) instead of rejecting, so requests parked behind a slow model
	// load or a long generation wait their turn rather than 429ing, and never
	// race into a slot the backend can't serve concurrently. See serializeSlot.
	if p.serializeSlot != nil {
		select {
		case p.serializeSlot <- struct{}{}:
			defer func() { <-p.serializeSlot }()
		case <-r.Context().Done():
			return
		}
	}

	// Hard request-time cap: bound the upstream request so a wedged backend (a
	// GPU-kernel deadlock — pinned at 100% but producing nothing) can't hang the
	// client forever. On expiry the reverse proxy's round-trip is cancelled and
	// its ErrorHandler returns an error to the client — real feedback instead of
	// an infinite spinner — and the slot recovery below fires.
	serveR := r
	if secs := p.config.MaxRequestTimeSecs; secs > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(secs)*time.Second)
		defer cancel()
		serveR = r.WithContext(ctx)
	}
	(*fn)(w, serveR)

	// If the request ended with its context cancelled — the client disconnected
	// mid-stream OR the hard timeout fired — and this was the only in-flight
	// request, run slot recovery: cancel orphaned llama.cpp slots and, if a slot
	// stays wedged (cancel ignored, the GPU-kernel-deadlock signature), restart
	// the backend so the next request reloads clean. Only llama.cpp has /slots.
	if p.config.IsLlamaCpp() && serveR.Context().Err() != nil && p.inflight.Load() <= 1 {
		go p.cancelBusySlots()
	}
}

// cancelBusySlots queries the upstream llama.cpp /slots endpoint and cancels
// every slot currently processing. Called when a request ended with its
// context cancelled (client disconnect OR the maxRequestTimeSecs hard timeout)
// so orphaned inference does not burn GPU or block the next request. It then
// verifies the cancel actually released the slot: a backend wedged in a GPU
// kernel keeps the slot processing (GPU pinned) while its HTTP control plane
// still answers — the cancel is ignored. When that happens, or when the
// backend stops answering at all, it restarts the process so the next request
// reloads clean instead of hanging on a wedged backend. Requires the upstream
// llama-server to have the /slots endpoint enabled.
func (p *ProcessCommand) cancelBusySlots() {
	base := strings.TrimRight(p.config.Proxy, "/")
	if base == "" {
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}

	busy, err := p.hasProcessingSlot(base, client)
	if err != nil {
		// HTTP control plane unresponsive — a hung/wedged backend. Restart so
		// the next request gets a fresh process instead of hanging behind it.
		p.proxyLogger.Warnf("<%s> cancelBusySlots: GET /slots failed (%v) — backend appears hung, restarting", p.id, err)
		go p.Stop(10 * time.Second)
		return
	}
	if !busy {
		return
	}

	resp, err := client.Get(base + "/slots")
	if err != nil {
		return
	}
	var slots []struct {
		ID    int `json:"id"`
		State int `json:"state"`
	}
	dErr := json.NewDecoder(resp.Body).Decode(&slots)
	resp.Body.Close()
	if dErr != nil {
		p.proxyLogger.Debugf("<%s> cancelBusySlots: decode: %v", p.id, dErr)
		return
	}
	for _, slot := range slots {
		if slot.State != 1 { // only cancel processing slots
			continue
		}
		cancelURL := fmt.Sprintf("%s/slots/%d", base, slot.ID)
		req, err := http.NewRequest(http.MethodDelete, cancelURL, bytes.NewBufferString(`{"action":"cancel"}`))
		if err != nil {
			continue
		}
		if cancelResp, err := client.Do(req); err == nil {
			cancelResp.Body.Close()
			p.proxyLogger.Infof("<%s> cancelBusySlots: cancelled orphaned slot %d", p.id, slot.ID)
		}
	}

	// Verify the cancel took effect. If the slot is still processing after a
	// short grace AND no new request has taken it over, the cancel was ignored
	// (the GPU-kernel-wedge signature) — restart rather than leave the GPU
	// pinned on dead work indefinitely.
	for attempt := 0; attempt < 3; attempt++ {
		time.Sleep(2 * time.Second)
		if p.inflight.Load() > 0 {
			return // a new request legitimately owns a slot again
		}
		busy, err := p.hasProcessingSlot(base, client)
		if err != nil {
			p.proxyLogger.Warnf("<%s> cancelBusySlots: backend unresponsive after cancel (%v) — restarting", p.id, err)
			go p.Stop(10 * time.Second)
			return
		}
		if !busy {
			return // cancel took effect, slot released
		}
	}
	p.proxyLogger.Warnf("<%s> cancelBusySlots: slot still processing ~6s after cancel with no client — backend wedged, restarting", p.id)
	go p.Stop(10 * time.Second)
}

// hasProcessingSlot reports whether any llama.cpp slot is in the processing
// state (1), used to verify a slot-cancel actually released the slot.
func (p *ProcessCommand) hasProcessingSlot(base string, client *http.Client) (bool, error) {
	resp, err := client.Get(base + "/slots")
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var slots []struct {
		State int `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&slots); err != nil {
		return false, err
	}
	for _, s := range slots {
		if s.State == 1 {
			return true, nil
		}
	}
	return false, nil
}

// recordUnexpectedExit appends a crash timestamp, prunes entries older than
// crashLoopWindow, and returns the current count.
func (p *ProcessCommand) recordUnexpectedExit() int {
	p.crashMu.Lock()
	defer p.crashMu.Unlock()
	now := time.Now()
	kept := p.crashTimes[:0]
	for _, t := range p.crashTimes {
		if now.Sub(t) <= crashLoopWindow {
			kept = append(kept, t)
		}
	}
	p.crashTimes = append(kept, now)
	return len(p.crashTimes)
}

func (p *ProcessCommand) clearCrashHistory() {
	p.crashMu.Lock()
	p.crashTimes = nil
	p.crashMu.Unlock()
}

// crashLoopError returns a non-nil error when the upstream has crashed at
// least crashLoopThreshold times within crashLoopWindow and the most recent
// crash was less than crashLoopCooldown ago. The error reaches clients via
// the normal swap-failure path, replacing the silent restart-on-every-request
// behaviour a crash-looping backend would otherwise get.
func (p *ProcessCommand) crashLoopError() error {
	p.crashMu.Lock()
	defer p.crashMu.Unlock()
	now := time.Now()
	count := 0
	var last time.Time
	for _, t := range p.crashTimes {
		if now.Sub(t) <= crashLoopWindow {
			count++
			if t.After(last) {
				last = t
			}
		}
	}
	if count < crashLoopThreshold {
		return nil
	}
	wait := crashLoopCooldown - now.Sub(last)
	if wait <= 0 {
		return nil
	}
	return fmt.Errorf(
		"[%s] upstream crashed %d times in the last %v; refusing restart for another %v (check the model's logs and system memory, or unload the model to reset)",
		p.id, count, crashLoopWindow, wait.Round(time.Second),
	)
}

// inferenceProbeLoop periodically sends a 1-token completion to the backend
// while the process is ready and idle. After inferenceProbeThreshold
// consecutive failures the process is stopped so the next request triggers a
// clean restart (with its loading banner) instead of hanging forever against
// a backend whose generation thread has died. Self-terminates when the
// process leaves StateReady.
func (p *ProcessCommand) inferenceProbeLoop() {
	ticker := time.NewTicker(p.probeInterval)
	defer ticker.Stop()

	failures := 0
	for range ticker.C {
		if p.State() != StateReady {
			return
		}
		// Only probe an idle backend: a long-running real request would
		// queue the probe behind it and time out spuriously.
		if p.inflight.Load() != 0 {
			failures = 0
			continue
		}
		// Take the serialization slot (non-blocking) so the probe can never
		// run concurrently with a real request — simultaneous requests are
		// exactly what crashes mlx_lm.server. Slot busy means the backend is
		// working; that answers the health question for this round.
		if p.serializeSlot != nil {
			select {
			case p.serializeSlot <- struct{}{}:
			default:
				failures = 0
				continue
			}
		}
		lastUseBefore := p.lastUse.Load()
		ctx, cancel := context.WithTimeout(p.parentCtx, p.probeTimeout)
		err := p.warmupModel(ctx)
		cancel()
		if p.serializeSlot != nil {
			<-p.serializeSlot
		}
		if p.State() != StateReady {
			return
		}
		if err == nil {
			failures = 0
			continue
		}
		// A real request completed while we probed — the busy backend is
		// the likely cause of the failure. Inconclusive; retry.
		if p.lastUse.Load() != lastUseBefore {
			continue
		}
		failures++
		p.proxyLogger.Warnf("<%s> inference probe failed (%d/%d): %v", p.id, failures, inferenceProbeThreshold, err)
		if failures < inferenceProbeThreshold {
			continue
		}
		p.proxyLogger.Errorf("<%s> backend accepts connections but does not answer inference; stopping so the next request restarts it", p.id)
		p.Stop(10 * time.Second)
		return
	}
}

// warmupModel sends a minimal chat completion to the backend to trigger eager
// model loading. MLX and vLLM report /health OK before weights are in memory,
// so without this the first real request would block for the full load time.
func (p *ProcessCommand) warmupModel(ctx context.Context) error {
	modelID := p.backendModelID(ctx)
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"."}],"max_tokens":1,"stream":false}`, modelID)
	req, err := http.NewRequestWithContext(ctx, "POST", p.config.Proxy+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("warm-up request returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// backendModelID returns the model ID the backend server knows about by
// querying /v1/models. Falls back to UseModelName (if set) then to p.id.
func (p *ProcessCommand) backendModelID(ctx context.Context) string {
	if p.config.UseModelName != "" {
		return p.config.UseModelName
	}
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", p.config.Proxy+"/v1/models", nil)
	if err != nil {
		return p.id
	}
	resp, err := client.Do(req)
	if err != nil {
		return p.id
	}
	defer resp.Body.Close()
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &result); err != nil {
		return p.id
	}
	if len(result.Data) > 0 && result.Data[0].ID != "" {
		return result.Data[0].ID
	}
	return p.id
}
