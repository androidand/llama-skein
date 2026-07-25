package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/androidand/llama-skein/internal/logmon"
)

var loadingPaths = []string{
	"/v1/chat/completions",
}

func isLoadingPath(path string) bool {
	for _, p := range loadingPaths {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// defaultCommitAfter bounds how long loading state is buffered before the
// response is committed. Chosen well under a typical client header timeout
// (opencode's default is 180s) so a slow-but-healthy load is never starved of
// bytes, while still covering the fast-failure class — exec errors, fit
// refusals and OOMs — which is where the 200-with-no-deltas bug actually bit.
const defaultCommitAfter = 30 * time.Second

type loadingWriter struct {
	hasWritten bool
	writer     http.ResponseWriter
	req        *http.Request
	ctx        context.Context
	logger     *logmon.Monitor
	modelName  string
	theme      LoadingTheme
	startTime  time.Time

	pendingMu     sync.Mutex
	pendingUpdate string

	// writeMu serializes writes to the underlying writer and guards released,
	// committed and buf. Once released is set, the streaming goroutine must
	// not touch the writer again — ServeHTTP has reclaimed it (to run the real
	// handler or to return) and writing/flushing a finalized response panics.
	writeMu  sync.Mutex
	released bool

	// Deferred commitment. Until committed, loading chatter accumulates in buf
	// and nothing reaches the client, so the status code is still ours to
	// choose. Previously the 200 went out at construction, before the load
	// outcome was known: when the load then failed, SendError's WriteHeader
	// was a no-op on an already-committed response and the error was appended
	// into the SSE stream instead. The caller saw 200 OK, a long duration and
	// zero valid deltas — a success-shaped non-answer.
	//
	// commitAfter bounds the wait. A load slower than this commits anyway to
	// keep the connection alive, because a client that receives no bytes will
	// hit its own header timeout (opencode's default is 180s) and give up on a
	// load that was going to succeed. So: failures faster than commitAfter get
	// a real status code — the exec errors, fit refusals and OOMs that make up
	// the common case — while a backend that wedges mid-load is left to the
	// request-timeout and watchdog machinery, which is what actually handles it.
	committed   bool
	discarded   bool
	buf         bytes.Buffer
	commitAfter time.Duration

	// closed by start when the goroutine finishes (after cleanup messages)
	done chan struct{}

	// test-only: closed when start enters its loop
	loopStarted chan struct{}
	// test-only: override the 1s tick interval
	tickDuration time.Duration
	// test-only: override character streaming speed (0 = no delay)
	charPerSecond float64
}

func newLoadingWriter(logger *logmon.Monitor, modelName string, theme LoadingTheme, w http.ResponseWriter, req *http.Request) *loadingWriter {
	s := &loadingWriter{
		writer:        w,
		req:           req,
		ctx:           req.Context(),
		logger:        logger,
		modelName:     modelName,
		theme:         theme,
		startTime:     time.Now(),
		tickDuration:  750 * time.Millisecond,
		charPerSecond: 75,
	}

	s.commitAfter = defaultCommitAfter
	// Headers are staged, not sent: WriteHeader is deliberately not called here.
	// commit() sends them once we know the load is going to produce something.
	s.Header().Set("Content-Type", "text/event-stream")
	s.Header().Set("Cache-Control", "no-cache")
	s.Header().Set("Connection", "keep-alive")
	s.sendLine("━━━━━")
	s.sendLine(fmt.Sprintf("llama-skein loading model: %s", modelName))
	return s
}

// commit releases everything buffered so far to the client and hands the
// writer over to normal streaming. Idempotent.
func (s *loadingWriter) commit() {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.commitLocked()
}

func (s *loadingWriter) commitLocked() {
	// Deliberately not gated on released. release() fences off the *background*
	// streaming goroutine; commit() is called from ServeHTTP's own goroutine
	// after that fence, which is precisely when the writer is safely ours.
	// Gating on it here would silently drop the buffer on every success.
	if s.committed || s.discarded {
		return
	}
	s.committed = true
	if !s.hasWritten {
		s.hasWritten = true
		s.writer.WriteHeader(http.StatusOK)
	}
	if s.buf.Len() > 0 {
		if _, err := s.writer.Write(s.buf.Bytes()); err != nil {
			s.logger.Debugf("<%s> failed flushing buffered loading state: %v", s.modelName, err)
		}
		s.buf.Reset()
	}
	if flusher, ok := s.writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

// discard throws away the buffered loading state without writing anything, so
// the caller can still set a real status code. Returns false if the response
// was already committed and the status is therefore no longer ours to choose.
func (s *loadingWriter) discard() bool {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.committed {
		return false
	}
	s.discarded = true
	s.buf.Reset()
	return true
}

func (s *loadingWriter) setUpdate(msg string) {
	s.pendingMu.Lock()
	s.pendingUpdate = msg
	s.pendingMu.Unlock()
}

func (s *loadingWriter) start(ctx context.Context) {
	s.done = make(chan struct{})
	defer close(s.done)

	defer func() {
		// Skip cleanup writes if the client disconnected — the connection
		// is being torn down and flushing against it will panic.
		if s.ctx.Err() != nil {
			return
		}
		duration := time.Since(s.startTime)
		s.sendData("\n")
		s.sendLine(fmt.Sprintf("Done! (%.2fs)", duration.Seconds()))
		s.sendLine("━━━━━")
		s.sendLine(" ")
	}()

	src := resolveThemeRemarks(s.theme)
	remarks := make([]string, len(src))
	copy(remarks, src)
	rand.Shuffle(len(remarks), func(i, j int) {
		remarks[i], remarks[j] = remarks[j], remarks[i]
	})
	ri := 0

	nextRemarkIn := time.Duration(2+rand.Intn(4)) * time.Second
	lastRemarkTime := time.Time{}

	ticker := time.NewTicker(s.tickDuration)
	defer ticker.Stop()

	if s.loopStarted != nil {
		close(s.loopStarted)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pendingMu.Lock()
			update := s.pendingUpdate
			s.pendingUpdate = ""
			s.pendingMu.Unlock()

			if update != "" {
				s.sendData("\n")
				s.sendInline(update)
				s.sendData(" ")
				lastRemarkTime = time.Now()
				nextRemarkIn = time.Duration(5+rand.Intn(5)) * time.Second
			} else if time.Since(lastRemarkTime) >= nextRemarkIn {
				remark := remarks[ri%len(remarks)]
				ri++
				s.sendData("\n")
				s.sendInline(remark)
				s.sendData(" ")
				lastRemarkTime = time.Now()
				nextRemarkIn = time.Duration(5+rand.Intn(5)) * time.Second
			} else {
				s.sendData(".")
			}
		}
	}
}

func (s *loadingWriter) waitForCompletion(timeout time.Duration) bool {
	if s.done == nil {
		return true
	}
	select {
	case <-s.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (s *loadingWriter) sendInline(text string) {
	chunkSize := 10
	if s.charPerSecond > 0 {
		chunkSize = max(3, int(s.charPerSecond)/15)
	}

	runes := []rune(text)
	for i := 0; i < len(runes); {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		end := i + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunk := string(runes[i:end])
		s.sendData(chunk)
		i = end

		if i < len(runes) && s.charPerSecond > 0 {
			time.Sleep(time.Duration(float64(time.Second) * float64(len(chunk)) / s.charPerSecond))
		}
	}
}

func (s *loadingWriter) sendLine(line string) {
	if line == "" {
		s.sendData("\n")
		return
	}
	s.sendInline(line)
	s.sendData("\n")
}

func (s *loadingWriter) sendData(data string) {
	type Delta struct {
		ReasoningContent string `json:"reasoning_content"`
	}
	type Choice struct {
		Delta Delta `json:"delta"`
	}
	type SSEMessage struct {
		Choices []Choice `json:"choices"`
		// skein_loading marks this as a llama-skein loading-state chunk, not
		// real model output. Clients (opencode-skein) should render it live as
		// a progress indicator but NOT persist it — these themes are pure UI
		// flavor and otherwise bloat the session store. Standard OpenAI clients
		// ignore the unknown field and still show it as reasoning, so the
		// marker is backward-compatible.
		SkeinLoading bool `json:"skein_loading"`
	}

	msg := SSEMessage{
		Choices: []Choice{
			{
				Delta: Delta{
					ReasoningContent: data,
				},
			},
		},
		SkeinLoading: true,
	}

	jsonData, err := json.Marshal(msg)
	if err != nil {
		s.logger.Errorf("<%s> Failed to marshal SSE message: %v", s.modelName, err)
		return
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// Once ServeHTTP has reclaimed the writer (release), writing/flushing it
	// races the real handler or panics on a finalized response. Stop here.
	if s.released || s.discarded {
		return
	}

	// Before commitment the chatter accumulates in memory, so a load that
	// fails leaves the status code still ours to set.
	if !s.committed {
		if time.Since(s.startTime) < s.commitAfter {
			fmt.Fprintf(&s.buf, "data: %s\n\n", jsonData)
			return
		}
		// Slow load: commit so the client keeps the connection open.
		s.commitLocked()
	}

	if _, err = fmt.Fprintf(s.writer, "data: %s\n\n", jsonData); err != nil {
		s.logger.Debugf("<%s> Failed to write SSE data (client likely disconnected): %v", s.modelName, err)
		return
	}
	if flusher, ok := s.writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

// release fences the loadingWriter off from the underlying ResponseWriter.
// After it returns, the streaming goroutine will not write to or flush the
// writer again: any in-flight write completes under writeMu first, and later
// writes short-circuit on released. The caller can then safely hand the writer
// to the real handler or let ServeHTTP return without racing a finalized
// response (a use-after-return Flush panics on the recycled *bufio.Writer).
func (s *loadingWriter) release() {
	s.writeMu.Lock()
	s.released = true
	s.writeMu.Unlock()
}

func (s *loadingWriter) Header() http.Header {
	return s.writer.Header()
}

func (s *loadingWriter) Write(data []byte) (int, error) {
	return s.writer.Write(data)
}

func (s *loadingWriter) WriteHeader(statusCode int) {
	if s.hasWritten {
		return
	}
	s.hasWritten = true
	s.writer.WriteHeader(statusCode)
	s.Flush()
}

func (s *loadingWriter) Flush() {
	if flusher, ok := s.writer.(http.Flusher); ok {
		flusher.Flush()
	}
}
