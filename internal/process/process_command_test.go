package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/logmon"
)

const (
	testStartTimeout    = 3 * time.Second
	testStopTimeout     = 2 * time.Second
	testReturnTimeout   = 1 * time.Second
	testPollInterval    = 20 * time.Millisecond
	testLogPollInterval = 10 * time.Millisecond
)

func newProcessCommand(t *testing.T, conf config.ModelConfig) *ProcessCommand {
	t.Helper()
	logger := logmon.NewWriter(io.Discard)
	p, err := New(context.Background(), t.Name(), conf, logger, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Tests run a mock proxy server on the proxy port (production has the model's
	// own upstream there); don't let pre-start reclaim kill the mock.
	p.skipPortReclaim = true
	return p
}

// runAsync starts Run in a goroutine and waits until the process is ready,
// matching the new interface contract where Run blocks until the process is
// terminated. Returns a channel that delivers Run's eventual error.
func runAsync(t *testing.T, p *ProcessCommand) <-chan error {
	t.Helper()
	ch := make(chan error, 1)
	go func() { ch <- p.Run(testStartTimeout) }()
	ctx, cancel := context.WithTimeout(context.Background(), testStartTimeout)
	defer cancel()
	if err := p.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	return ch
}

func TestProcessCommand_StartStop(t *testing.T) {
	skipIfNoSimpleResponder(t)

	cmd, port := simpleResponderCmd(t, "-silent", "-respond hello")
	p := newProcessCommand(t, config.ModelConfig{
		Cmd:                cmd,
		Proxy:              fmt.Sprintf("http://127.0.0.1:%d", port),
		CheckEndpoint:      "/health",
		HealthCheckTimeout: 10,
	})
	t.Cleanup(func() { p.Stop(testStopTimeout) })

	req := httptest.NewRequest("GET", "/test", nil)

	// before start: no handler
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("before start: expected 503, got %d", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, "llama-skein-error") {
		t.Errorf("before start: expected body to contain %q, got %q", "llama-skein-error", body)
	}

	runErr := runAsync(t, p)
	if got := p.State(); got != StateReady {
		t.Errorf("after Run: expected state %s, got %s", StateReady, got)
	}

	rr = httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("after Run: expected 200, got %d", rr.Code)
	}
	if body := rr.Body.String(); body != "hello" {
		t.Errorf("expected body %q, got %q", "hello", body)
	}

	if err := p.Stop(testStopTimeout); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
	if got := p.State(); got != StateStopped {
		t.Errorf("after Stop: expected state %s, got %s", StateStopped, got)
	}
	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("Run() after Stop: expected nil, got %v", err)
		}
	case <-time.After(testReturnTimeout):
		t.Fatal("Run() did not return after Stop")
	}

	// after stop: handler cleared
	rr = httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("after stop: expected 503, got %d", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, "llama-skein-error") {
		t.Errorf("after stop: expected body to contain %q, got %q", "llama-skein-error", body)
	}
}

func TestProcessCommand_Run_Idempotent(t *testing.T) {
	skipIfNoSimpleResponder(t)

	cmd, port := simpleResponderCmd(t, "-silent")
	p := newProcessCommand(t, config.ModelConfig{
		Cmd:                cmd,
		Proxy:              fmt.Sprintf("http://127.0.0.1:%d", port),
		CheckEndpoint:      "/health",
		HealthCheckTimeout: 10,
	})
	t.Cleanup(func() { p.Stop(testStopTimeout) })

	runErr := runAsync(t, p)

	if err := p.Run(testStartTimeout); err == nil {
		t.Error("second Run() while running: expected error, got nil")
	}

	if err := p.Stop(testStopTimeout); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
	select {
	case <-runErr:
	case <-time.After(testReturnTimeout):
		t.Fatal("Run() did not return after Stop")
	}
}

func TestProcessCommand_Stop_Idempotent(t *testing.T) {
	skipIfNoSimpleResponder(t)

	cmd, port := simpleResponderCmd(t, "-silent")
	p := newProcessCommand(t, config.ModelConfig{
		Cmd:                cmd,
		Proxy:              fmt.Sprintf("http://127.0.0.1:%d", port),
		CheckEndpoint:      "/health",
		HealthCheckTimeout: 10,
	})

	if err := p.Stop(testStopTimeout); err != nil {
		t.Fatalf("Stop() before Run(): %v", err)
	}

	runErr := runAsync(t, p)

	if err := p.Stop(testStopTimeout); err != nil {
		t.Fatalf("first Stop() error: %v", err)
	}
	select {
	case <-runErr:
	case <-time.After(testReturnTimeout):
		t.Fatal("Run() did not return after Stop")
	}

	if err := p.Stop(testStopTimeout); err != nil {
		t.Fatalf("second Stop() error: %v", err)
	}
}

// TestProcessCommand_StopCancelsRun verifies that a Stop sent while Run is
// executing its health-check loop returns ErrAbort to the Run caller.
//
// A blocking mock HTTP server is used as the proxy so the test can deterministically
// know when doStart is inside the health-check loop before issuing Stop.
func TestProcessCommand_StopCancelsRun(t *testing.T) {
	skipIfNoSimpleResponder(t)

	healthCheckStarted := make(chan struct{}, 1)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Signal that a health check is in-flight, then block until the client
		// cancels (which happens when Stop cancels the start context).
		select {
		case healthCheckStarted <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		http.Error(w, "mock cancelled", http.StatusServiceUnavailable)
	}))
	defer mock.Close()

	// simple-responder is the real process; health checks go to the blocking mock.
	cmd, _ := simpleResponderCmd(t, "-silent")
	p := newProcessCommand(t, config.ModelConfig{
		Cmd:                cmd,
		Proxy:              mock.URL,
		CheckEndpoint:      "/health",
		HealthCheckTimeout: 30,
	})

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- p.Run(testStartTimeout)
	}()

	// Block until doStart is actually performing a health check, guaranteeing
	// that Run is in-flight when Stop is called.
	<-healthCheckStarted

	if err := p.Stop(testStopTimeout); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	if err := <-runErrCh; !errors.Is(err, ErrStartAborted) {
		t.Errorf("expected ErrStartAborted from Run, got %v", err)
	}
}

// TestProcessCommand_ParentCtxCancelDuringStart verifies that cancelling the
// parent context while doStart is health-checking causes the process to
// transition to StateShutdown promptly, not wait for the health-check timeout.
//
// This is the config-reload race: Stop() returns early when parentCtx is
// already done and never writes to stopCh, so without a parentCtx.Done()
// case in the inner select, the process would keep loading indefinitely.
func TestProcessCommand_ParentCtxCancelDuringStart(t *testing.T) {
	skipIfNoSimpleResponder(t)

	healthCheckStarted := make(chan struct{}, 1)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case healthCheckStarted <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		http.Error(w, "mock cancelled", http.StatusServiceUnavailable)
	}))
	defer mock.Close()

	parentCtx, cancelParent := context.WithCancel(context.Background())
	logger := logmon.NewWriter(io.Discard)
	cmd, _ := simpleResponderCmd(t, "-silent")
	p, err := New(parentCtx, t.Name(), config.ModelConfig{
		Cmd:                cmd,
		Proxy:              mock.URL,
		CheckEndpoint:      "/health",
		HealthCheckTimeout: 60,
	}, logger, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErrCh := make(chan error, 1)
	go func() { runErrCh <- p.Run(60 * time.Second) }()

	<-healthCheckStarted

	// Cancel parent context to simulate a config reload tearing down the old server.
	cancelParent()

	select {
	case err := <-runErrCh:
		if !strings.Contains(err.Error(), "shutdown") {
			t.Errorf("Run error = %v, want shutdown error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("process did not shut down within 5s after parent context cancel during start")
	}

	// Run() may return before the run() goroutine writes StateShutdown;
	// poll briefly to avoid a spurious race in the assertion.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.State() == StateShutdown {
			break
		}
		time.Sleep(testPollInterval)
	}
	if got := p.State(); got != StateShutdown {
		t.Errorf("after cancel: expected StateShutdown, got %s", got)
	}
}

// TestProcessCommand_RunStopCycle runs several sequential start/stop pairs on
// fresh processes to confirm they are reusable.
func TestProcessCommand_RunStopCycle(t *testing.T) {
	skipIfNoSimpleResponder(t)

	for i := range 3 {
		cmd, port := simpleResponderCmd(t, "-silent")
		p := newProcessCommand(t, config.ModelConfig{
			Cmd:                cmd,
			Proxy:              fmt.Sprintf("http://127.0.0.1:%d", port),
			CheckEndpoint:      "/health",
			HealthCheckTimeout: 10,
		})

		runErr := runAsync(t, p)

		req := httptest.NewRequest("GET", "/health", nil)
		rr := httptest.NewRecorder()
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("cycle %d: expected 200 from /health, got %d", i, rr.Code)
		}

		if err := p.Stop(testStopTimeout); err != nil {
			t.Fatalf("cycle %d Stop() error: %v", i, err)
		}
		select {
		case <-runErr:
		case <-time.After(testReturnTimeout):
			t.Fatalf("cycle %d: Run() did not return after Stop", i)
		}
	}
}

// TestProcessCommand_ReverseProxyPanicIsRecovered drives the full proxy path:
// the upstream responds healthy on /health (so Run completes), then on the
// actual proxied request it hijacks the connection and closes it mid-body.
// That upstream EOF makes httputil.ReverseProxy.copyResponse return an error,
// which panics with http.ErrAbortHandler — the wrapped handlerFn must recover
// and log the disconnect.
//
// Requests are issued through an httptest.NewServer wrapping the process so
// the panic actually fires (httputil only panics on copy errors when the
// request carries http.ServerContextKey, which a real server sets).
//
// see: https://github.com/golang/go/issues/23643
func TestProcessCommand_ReverseProxyPanicIsRecovered(t *testing.T) {
	skipIfNoSimpleResponder(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// Send a Content-Length that promises 100 bytes, deliver only a few,
		// then slam the connection shut. The reverse proxy will see EOF
		// before the body is fully copied and panic with ErrAbortHandler.
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("upstream: hijack not supported")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("upstream: hijack: %v", err)
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 100\r\nContent-Type: text/plain\r\n\r\npartial"))
		_ = conn.Close()
	}))
	t.Cleanup(upstream.Close)

	// Capture proxy log output so we can assert the recover message was
	// emitted by handlerFn.
	logBuf := &syncBuffer{}
	proxyLogger := logmon.NewWriter(logBuf)
	procLogger := logmon.NewWriter(io.Discard)

	cmd, _ := simpleResponderCmd(t, "-silent")
	p, err := New(context.Background(), t.Name(), config.ModelConfig{
		Cmd:                cmd,
		Proxy:              upstream.URL,
		CheckEndpoint:      "/health",
		HealthCheckTimeout: 10,
	}, procLogger, proxyLogger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { p.Stop(testStopTimeout) })

	_ = runAsync(t, p)

	// Wrap p in an httptest server so requests get http.ServerContextKey
	// automatically — that is what makes httputil.ReverseProxy raise the panic.
	front := httptest.NewServer(p)
	t.Cleanup(front.Close)

	resp, err := http.Get(front.URL + "/disconnect")
	if err == nil {
		resp.Body.Close()
	}

	const want = "recovered from upstream disconnection"
	deadline := time.Now().Add(testReturnTimeout)
	for time.Now().Before(deadline) {
		if strings.Contains(logBuf.String(), want) {
			return
		}
		time.Sleep(testLogPollInterval)
	}
	t.Errorf("expected proxy log to contain %q; got:\n%s", want, logBuf.String())
}

// syncBuffer is a concurrent-safe bytes.Buffer for capturing logmon output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestProcessCommand_TTL_StopsAfterIdle verifies that a process with a TTL
// automatically stops itself after the idle timeout has elapsed following its
// last request.
func TestProcessCommand_TTL_StopsAfterIdle(t *testing.T) {
	skipIfNoSimpleResponder(t)

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mock.Close)

	cmd, _ := simpleResponderCmd(t, "-silent")

	cfg := config.ModelConfig{
		Cmd:                cmd,
		Proxy:              mock.URL,
		CheckEndpoint:      "/health",
		HealthCheckTimeout: 10,
		UnloadAfter:        1, // 1-second TTL
	}
	if runtime.GOOS == "windows" {
		cfg.CmdStop = "taskkill /f /t /pid ${PID}"
	}

	p := newProcessCommand(t, cfg)

	runErr := runAsync(t, p)
	defer func() {
		if p.State() == StateReady {
			p.Stop(testStopTimeout)
		}
	}()

	if got := p.State(); got != StateReady {
		t.Fatalf("expected StateReady, got %s", got)
	}

	// Make one request to prime the last-use timestamp.
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 after request, got %d", rr.Code)
	}

	// Wait for the TTL goroutine to fire and the process to fully stop.
	// Poll for StateStopped directly to avoid racing the StateStopping
	// intermediate state that sits between StateReady and StateStopped.
	deadline := time.Now().Add(5 * time.Second)
	for p.State() != StateStopped && time.Now().Before(deadline) {
		time.Sleep(testPollInterval)
	}

	if got := p.State(); got != StateStopped {
		t.Fatalf("TTL did not stop process; state is %s (expected %s)", got, StateStopped)
	}

	// Run() should have returned nil (clean stop from TTL).
	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("Run() after TTL stop: expected nil, got %v", err)
		}
	case <-time.After(testReturnTimeout):
		t.Fatal("Run() did not return after TTL-induced stop")
	}
}

// TestProcessCommand_TTL_ResetsOnRequest verifies that inflight requests
// prevent the TTL goroutine from stopping the process, and that the TTL timer
// resets after each request completes.
func TestProcessCommand_TTL_ResetsOnRequest(t *testing.T) {
	skipIfNoSimpleResponder(t)

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mock.Close)

	cmd, _ := simpleResponderCmd(t, "-silent")
	p := newProcessCommand(t, config.ModelConfig{
		Cmd:                cmd,
		Proxy:              mock.URL,
		CheckEndpoint:      "/health",
		HealthCheckTimeout: 10,
		UnloadAfter:        1, // 1-second TTL
	})

	runErr := runAsync(t, p)
	defer func() {
		if p.State() == StateReady {
			p.Stop(testStopTimeout)
		}
	}()

	// Keep sending requests for 1.5s — past the 1s TTL — and verify
	// the process never stops while traffic is flowing.
	stopAt := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(stopAt) {
		req := httptest.NewRequest("GET", "/", nil)
		rr := httptest.NewRecorder()
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rr.Code)
		}
		if p.State() != StateReady {
			t.Fatalf("process was stopped during active traffic (state=%s)", p.State())
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := p.State(); got != StateReady {
		t.Fatalf("expected StateReady while traffic was active, got %s", got)
	}

	// Now stop manually to clean up.
	if err := p.Stop(testStopTimeout); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
	select {
	case <-runErr:
	case <-time.After(testReturnTimeout):
		t.Fatal("Run() did not return after Stop")
	}
}

// TestProcessCommand_TTL_ZeroDisables verifies that UnloadAfter=0 does not
// spawn a TTL goroutine — the process stays ready until explicitly stopped.
func TestProcessCommand_TTL_ZeroDisables(t *testing.T) {
	skipIfNoSimpleResponder(t)

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mock.Close)

	cmd, _ := simpleResponderCmd(t, "-silent")
	p := newProcessCommand(t, config.ModelConfig{
		Cmd:                cmd,
		Proxy:              mock.URL,
		CheckEndpoint:      "/health",
		HealthCheckTimeout: 10,
		UnloadAfter:        0, // disabled
	})

	runErr := runAsync(t, p)
	defer func() {
		if p.State() == StateReady {
			p.Stop(testStopTimeout)
		}
	}()

	if got := p.State(); got != StateReady {
		t.Fatalf("expected StateReady, got %s", got)
	}

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 after request, got %d", rr.Code)
	}

	// No TTL goroutine is spawned when UnloadAfter=0, so a brief sleep is
	// enough to confirm the process remains ready.
	time.Sleep(100 * time.Millisecond)

	if got := p.State(); got != StateReady {
		t.Fatalf("process was stopped unexpectedly (state=%s) with TTL=0", got)
	}

	// Cleanly stop.
	if err := p.Stop(testStopTimeout); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
	select {
	case <-runErr:
	case <-time.After(testReturnTimeout):
		t.Fatal("Run() did not return after Stop")
	}
}

// TestProcessCommand_ConcurrentRunStop launches many concurrent run/stop racing
// pairs to exercise the race detector and verify no deadlocks occur.
func TestProcessCommand_ConcurrentRunStop(t *testing.T) {
	skipIfNoSimpleResponder(t)

	for range 10 {
		cmd, port := simpleResponderCmd(t, "-silent")
		cfg := config.ModelConfig{
			Cmd:                cmd,
			Proxy:              fmt.Sprintf("http://127.0.0.1:%d", port),
			CheckEndpoint:      "/health",
			HealthCheckTimeout: 10,
		}

		if runtime.GOOS == "windows" {
			cfg.CmdStop = "taskkill /f /t /pid ${PID}"
		}

		p := newProcessCommand(t, cfg)

		runDone := make(chan struct{})
		go func() {
			defer close(runDone)
			p.Run(testStartTimeout) //nolint: errcheck — one goroutine wins the race
		}()
		go func() {
			p.Stop(testStopTimeout) //nolint: errcheck
		}()

		// Backstop: the racing Stop may have arrived before Run got on the
		// channel (making it a no-op), so keep stopping until Run unblocks.
		deadline := time.After(testStartTimeout)
		for done := false; !done; {
			select {
			case <-runDone:
				done = true
			case <-deadline:
				t.Fatal("Run did not return")
			case <-time.After(testPollInterval):
				p.Stop(testStopTimeout) //nolint: errcheck
			}
		}
	}
}

// TestProcessCommand_CrashLoopBreaker verifies that repeated unexpected
// upstream exits trip the breaker (restarts refused with a clear error) and
// that an explicit Stop resets it.
func TestProcessCommand_CrashLoopBreaker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses the unix sleep command")
	}

	cfg := config.ModelConfig{
		Cmd:                "sleep 0.2",
		Proxy:              "http://127.0.0.1:1", // never contacted: checkEndpoint is none
		CheckEndpoint:      "none",
		HealthCheckTimeout: 10,
	}
	p := newProcessCommand(t, cfg)
	t.Cleanup(func() { p.Stop(testStopTimeout) })

	for i := 0; i < crashLoopThreshold; i++ {
		err := p.Run(testStartTimeout)
		if err == nil || !strings.Contains(err.Error(), "exited unexpectedly") {
			t.Fatalf("crash %d: expected unexpected-exit error, got %v", i+1, err)
		}
	}

	// breaker engaged: next Run is refused without starting the process
	err := p.Run(testStartTimeout)
	if err == nil || !strings.Contains(err.Error(), "refusing restart") {
		t.Fatalf("expected crash-loop refusal, got %v", err)
	}

	// explicit Stop resets the breaker; the process starts (and crashes) again
	if err := p.Stop(testStopTimeout); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	err = p.Run(testStartTimeout)
	if err == nil || !strings.Contains(err.Error(), "exited unexpectedly") {
		t.Fatalf("after reset: expected unexpected-exit error, got %v", err)
	}
}

// TestProcessCommand_InferenceProbe_StopsWedgedBackend verifies that a
// backend:mlx process whose /health stays 200 but whose inference endpoint
// stops answering is detected by the periodic probe and stopped, so the next
// request triggers a clean restart instead of hanging.
func TestProcessCommand_InferenceProbe_StopsWedgedBackend(t *testing.T) {
	skipIfNoSimpleResponder(t)

	var mu sync.Mutex
	wedged := false
	setWedged := func(v bool) { mu.Lock(); wedged = v; mu.Unlock() }
	isWedged := func() bool { mu.Lock(); defer mu.Unlock(); return wedged }

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" && isWedged() {
			http.Error(w, "generation thread is dead", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[]}`))
	}))
	t.Cleanup(mock.Close)

	cmd, _ := simpleResponderCmd(t, "-silent")
	cfg := config.ModelConfig{
		Cmd:                cmd,
		Proxy:              mock.URL,
		CheckEndpoint:      "/health",
		HealthCheckTimeout: 10,
		Backend:            config.BackendMLX,
		UseModelName:       "mlx-test", // skip the /v1/models lookup in probes
	}
	if runtime.GOOS == "windows" {
		cfg.CmdStop = "taskkill /f /t /pid ${PID}"
	}

	p := newProcessCommand(t, cfg)
	p.probeInterval = 50 * time.Millisecond
	p.probeTimeout = time.Second
	t.Cleanup(func() {
		if p.State() == StateReady {
			p.Stop(testStopTimeout)
		}
	})

	runErr := runAsync(t, p)

	// healthy backend: several probe ticks pass without stopping the process
	time.Sleep(250 * time.Millisecond)
	if got := p.State(); got != StateReady {
		t.Fatalf("expected StateReady with healthy probes, got %s", got)
	}

	// wedge the backend: inference fails while /health stays 200
	setWedged(true)

	deadline := time.Now().Add(5 * time.Second)
	for p.State() != StateStopped && time.Now().Before(deadline) {
		time.Sleep(testPollInterval)
	}
	if got := p.State(); got != StateStopped {
		t.Fatalf("expected probe to stop wedged process, got state %s", got)
	}

	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("Run after probe-stop: expected nil, got %v", err)
		}
	case <-time.After(testReturnTimeout):
		t.Fatal("Run() did not return after probe stopped the process")
	}
}

// TestProcessCommand_MLXSerializesRequests verifies that requests to a
// backend:mlx process are queued one-at-a-time at the process layer —
// simultaneous requests crash mlx_lm.server — while non-mlx requests are not
// serialized. Queueing (not 429) keeps slow loads from rejecting clients.
func TestProcessCommand_MLXSerializesRequests(t *testing.T) {
	skipIfNoSimpleResponder(t)

	var mu sync.Mutex
	inflight, maxInflight := 0, 0

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" && r.Method == "POST" {
			mu.Lock()
			inflight++
			if inflight > maxInflight {
				maxInflight = inflight
			}
			mu.Unlock()
			time.Sleep(30 * time.Millisecond)
			mu.Lock()
			inflight--
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[]}`))
	}))
	t.Cleanup(mock.Close)

	cmd, _ := simpleResponderCmd(t, "-silent")
	cfg := config.ModelConfig{
		Cmd:                cmd,
		Proxy:              mock.URL,
		CheckEndpoint:      "/health",
		HealthCheckTimeout: 10,
		Backend:            config.BackendMLX,
		UseModelName:       "mlx-test",
	}
	if runtime.GOOS == "windows" {
		cfg.CmdStop = "taskkill /f /t /pid ${PID}"
	}

	p := newProcessCommand(t, cfg)
	t.Cleanup(func() { p.Stop(testStopTimeout) })
	runAsync(t, p)

	// reset counters: the startup warmup also POSTs to the mock
	mu.Lock()
	inflight, maxInflight = 0, 0
	mu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
			p.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	wg.Wait()

	mu.Lock()
	got := maxInflight
	mu.Unlock()
	if got != 1 {
		t.Errorf("expected max 1 concurrent upstream request for mlx backend, got %d", got)
	}
}

// TestProcessCommand_ContextExceededMapsTo413 verifies that llama.cpp's
// exceed_context_size_error (HTTP 400) is remapped to 413 so callers can
// detect context overflow without parsing the body.
func TestProcessCommand_ContextExceededMapsTo413(t *testing.T) {
	skipIfNoSimpleResponder(t)

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"type":"exceed_context_size_error","message":"too big"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mock.Close)

	cmd, _ := simpleResponderCmd(t, "-silent")
	cfg := config.ModelConfig{
		Cmd:                cmd,
		Proxy:              mock.URL,
		CheckEndpoint:      "/health",
		HealthCheckTimeout: 10,
	}
	if runtime.GOOS == "windows" {
		cfg.CmdStop = "taskkill /f /t /pid ${PID}"
	}
	p := newProcessCommand(t, cfg)
	t.Cleanup(func() { p.Stop(testStopTimeout) })
	runAsync(t, p)

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 for exceed_context_size_error, got %d", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, "exceed_context_size_error") {
		t.Errorf("expected original body preserved, got %q", body)
	}
}

// TestProcessCommand_CancelsBusySlotsOnDisconnect verifies that a client
// disconnect mid-request triggers a cancel of processing llama.cpp slots.
func TestProcessCommand_CancelsBusySlotsOnDisconnect(t *testing.T) {
	skipIfNoSimpleResponder(t)

	cancelled := make(chan int, 4)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/slots" && r.Method == "GET":
			w.Write([]byte(`[{"id":0,"state":1},{"id":1,"state":0}]`))
		case strings.HasPrefix(r.URL.Path, "/slots/") && r.Method == "DELETE":
			var id int
			fmt.Sscanf(r.URL.Path, "/slots/%d", &id)
			cancelled <- id
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/v1/chat/completions":
			// slow generation: block until the client goes away
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(mock.Close)

	cmd, _ := simpleResponderCmd(t, "-silent")
	cfg := config.ModelConfig{
		Cmd:                cmd,
		Proxy:              mock.URL,
		CheckEndpoint:      "/health",
		HealthCheckTimeout: 10,
	}
	if runtime.GOOS == "windows" {
		cfg.CmdStop = "taskkill /f /t /pid ${PID}"
	}
	p := newProcessCommand(t, cfg)
	t.Cleanup(func() { p.Stop(testStopTimeout) })
	runAsync(t, p)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`)).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.ServeHTTP(httptest.NewRecorder(), req)
	}()
	time.Sleep(100 * time.Millisecond) // let the request reach the mock
	cancel()                           // simulate client disconnect
	<-done

	select {
	case id := <-cancelled:
		if id != 0 {
			t.Errorf("expected slot 0 cancelled, got %d", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no slot cancel received after client disconnect")
	}
}

// TestProcessCommand_Readiness_WarmupGatesReady verifies that for a non-llamacpp
// backend whose /health is 200 but whose inference (warm-up) fails, the process
// does NOT reach StateReady — readiness must mean resident-and-serving, not just
// a healthy HTTP server. Run must return an error rather than latch ready.
func TestProcessCommand_Readiness_WarmupGatesReady(t *testing.T) {
	skipIfNoSimpleResponder(t)

	// /health is healthy, but the warm-up chat completion always fails — the
	// "process up but model not resident" condition.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			http.Error(w, "model not loaded", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mock.Close)

	cmd, _ := simpleResponderCmd(t, "-silent")
	cfg := config.ModelConfig{
		Cmd:                cmd,
		Proxy:              mock.URL,
		CheckEndpoint:      "/health",
		HealthCheckTimeout: 10,
		Backend:            config.BackendMLX,
		UseModelName:       "mlx-test",
	}
	if runtime.GOOS == "windows" {
		cfg.CmdStop = "taskkill /f /t /pid ${PID}"
	}
	p := newProcessCommand(t, cfg)
	t.Cleanup(func() { p.Stop(testStopTimeout) })

	err := p.Run(testStartTimeout)
	if err == nil {
		t.Fatal("Run must fail when warm-up (residency) fails; got nil")
	}
	if !strings.Contains(err.Error(), "warm-up failed") {
		t.Errorf("expected warm-up failure error, got: %v", err)
	}
	if got := p.State(); got == StateReady {
		t.Errorf("process must NOT be StateReady when warm-up failed, got %s", got)
	}
}

// TestProcessCommand_Readiness_WarmupSuccessReady is the positive case: a backend
// whose warm-up succeeds reaches StateReady normally.
func TestProcessCommand_Readiness_WarmupSuccessReady(t *testing.T) {
	skipIfNoSimpleResponder(t)

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mock.Close)

	cmd, _ := simpleResponderCmd(t, "-silent")
	cfg := config.ModelConfig{
		Cmd:                cmd,
		Proxy:              mock.URL,
		CheckEndpoint:      "/health",
		HealthCheckTimeout: 10,
		Backend:            config.BackendMLX,
		UseModelName:       "mlx-test",
	}
	if runtime.GOOS == "windows" {
		cfg.CmdStop = "taskkill /f /t /pid ${PID}"
	}
	p := newProcessCommand(t, cfg)
	t.Cleanup(func() { p.Stop(testStopTimeout) })

	runErr := runAsync(t, p)
	if got := p.State(); got != StateReady {
		t.Errorf("expected StateReady after successful warm-up, got %s", got)
	}
	_ = runErr
}

func TestReclaimStalePort_Guards(t *testing.T) {
	// non-loopback host must never reclaim (don't touch remote/peer proxies)
	if n := reclaimStalePort("192.168.1.50:5900"); n != 0 {
		t.Errorf("non-loopback host must be a no-op, got %d", n)
	}
	// malformed input is a no-op
	if n := reclaimStalePort("garbage"); n != 0 {
		t.Errorf("malformed host:port must be a no-op, got %d", n)
	}
	// a free loopback port has nothing to reclaim
	if n := reclaimStalePort("127.0.0.1:1"); n != 0 {
		t.Errorf("free port must reclaim nothing, got %d", n)
	}
}
