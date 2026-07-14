package router

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/logmon"
	"github.com/androidand/llama-skein/internal/process"
)

// newQueuedReq builds a handlerReq as it would look parked in baseRouter's
// queued slice: queuedAt set, respond buffered (so expireStaleQueued's grant
// can complete without a live receiver goroutine — this test only checks
// which requests get errored/removed, not grant()'s liveness semantics).
func newQueuedReq(model string, queuedAt time.Time) handlerReq {
	return handlerReq{
		model:      model,
		ctx:        context.Background(),
		respond:    make(chan handlerResp, 1),
		positionCh: make(chan int, 1),
		queuedAt:   queuedAt,
	}
}

func newExpireTestRouter(evict map[string][]string) *baseRouter {
	conf := config.Config{HealthCheckTimeout: 5}
	planner := &stubPlanner{evict: evict}
	return newBaseRouter("test", conf, nil, planner, logmon.NewWriter(io.Discard))
}

func TestExpireStaleQueued_PartitionsExpiredFromFresh(t *testing.T) {
	b := newExpireTestRouter(map[string][]string{"a": {"busy"}})
	now := time.Now()
	timeout := 10 * time.Second

	expired := newQueuedReq("a", now.Add(-11*time.Second)) // queued 11s ago, past the 10s bound
	fresh := newQueuedReq("a", now.Add(-3*time.Second))    // queued 3s ago, within bound

	queued := []handlerReq{expired, fresh}
	b.expireStaleQueued(now, timeout, map[string]*activeSwap{}, &queued)

	if len(queued) != 1 || queued[0].queuedAt != fresh.queuedAt {
		t.Fatalf("expected only the fresh request to remain queued, got %d entries", len(queued))
	}

	select {
	case resp := <-expired.respond:
		if !errors.Is(resp.err, ErrSwapQueueTimeout) {
			t.Errorf("expired request got err=%v, want ErrSwapQueueTimeout", resp.err)
		}
	default:
		t.Error("expired request should have received an error response")
	}

	select {
	case resp := <-fresh.respond:
		t.Errorf("fresh request should NOT have been granted anything, got %+v", resp)
	default:
	}
}

func TestExpireStaleQueued_ErrorNamesBlockingModel(t *testing.T) {
	b := newExpireTestRouter(map[string][]string{"a": {"busy-model"}})
	now := time.Now()

	req := newQueuedReq("a", now.Add(-20*time.Second))
	queued := []handlerReq{req}
	b.expireStaleQueued(now, 10*time.Second, map[string]*activeSwap{}, &queued)

	resp := <-req.respond
	if resp.err == nil || !errors.Is(resp.err, ErrSwapQueueTimeout) {
		t.Fatalf("expected ErrSwapQueueTimeout, got %v", resp.err)
	}
	msg := resp.err.Error()
	if !strings.Contains(msg, "busy-model") {
		t.Errorf("error message %q does not name the blocking model", msg)
	}
}

func TestExpireStaleQueued_ZeroTimeoutDisablesBound(t *testing.T) {
	b := newExpireTestRouter(nil)
	now := time.Now()

	req := newQueuedReq("a", now.Add(-time.Hour)) // queued for an hour
	queued := []handlerReq{req}
	b.expireStaleQueued(now, 0, map[string]*activeSwap{}, &queued) // timeout disabled

	if len(queued) != 1 {
		t.Errorf("timeout=0 must disable the bound entirely; queue should be untouched, got %d entries", len(queued))
	}
	select {
	case resp := <-req.respond:
		t.Errorf("timeout=0 must never expire a request, got response %+v", resp)
	default:
	}
}

// TestBaseRouter_QueueTimeoutEndToEnd drives the real run() loop (ticker
// included) to prove the wiring, not just expireStaleQueued in isolation:
// model A is wedged (ServeHTTP blocks forever), a request for sibling model B
// queues behind it (case 5), and must come back with a 503
// ErrSwapQueueTimeout within a bounded, short time — not hang like z4 did.
func TestBaseRouter_QueueTimeoutEndToEnd(t *testing.T) {
	a := newFakeProcess("a")
	a.autoReady = true
	a.serveBlock = make(chan struct{}) // never closed — simulates a wedge
	bProc := newFakeProcess("b")

	planner := &stubPlanner{evict: map[string][]string{"b": {"a"}}}
	conf := config.Config{HealthCheckTimeout: 5, SwapQueueTimeoutSecs: 1}
	router := newBaseRouter("test", conf, map[string]process.Process{"a": a, "b": bProc}, planner, logmon.NewWriter(io.Discard))
	router.testProcessed = make(chan struct{}, 64)
	router.queueScanInterval = 50 * time.Millisecond
	go router.run()
	t.Cleanup(func() {
		if !router.shuttingDown.Load() {
			_ = router.Shutdown(time.Second)
		}
	})

	// Get A running and wedged inside ServeHTTP.
	wA := httptest.NewRecorder()
	go router.ServeHTTP(wA, newRequest("a"))
	waitProcessed(t, router.testProcessed, 1)
	<-a.runStarted
	a.markReady()
	<-a.serveStarted // A is now blocked inside ServeHTTP, holding inFlight["a"]

	// B queues behind A (case 5: would evict a busy process).
	wB := httptest.NewRecorder()
	doneB := make(chan struct{})
	go func() {
		router.ServeHTTP(wB, newRequest("b"))
		close(doneB)
	}()
	waitProcessed(t, router.testProcessed, 1)

	select {
	case <-doneB:
		if wB.Code != http.StatusServiceUnavailable {
			t.Fatalf("B status=%d body=%q, want 503", wB.Code, wB.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("B's request never resolved — the queue-timeout ticker did not fire (or is not wired into run())")
	}

	close(a.serveBlock) // release the wedged A so cleanup can Stop() it
}

func TestExpireStaleQueued_EmptyQueueIsNoop(t *testing.T) {
	b := newExpireTestRouter(nil)
	var queued []handlerReq
	b.expireStaleQueued(time.Now(), 10*time.Second, map[string]*activeSwap{}, &queued)
	if len(queued) != 0 {
		t.Errorf("expected empty queue to stay empty, got %d entries", len(queued))
	}
}
