package process

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/logmon"
)

// TestKillProcess_BoundedWaitOnUnreapableProcess is a regression for the z4
// wedge: killProcess's final wait for cmdDone (after SIGKILL) had no timeout.
// SIGKILL cannot interrupt a process blocked in an uninterruptible
// kernel-level wait (e.g. a GPU driver ioctl stuck behind a livelocked compute
// kernel), so if the OS never reaps the process, that wait — which runs
// inside the model's single in-order control loop — hung forever, freezing
// every future operation (start, stop, health checks) on that model. This
// simulates "the OS never reaps it" via a cmdDone channel that is never
// closed, and asserts killProcess gives up within its configured grace period
// instead of blocking indefinitely.
func TestKillProcess_BoundedWaitOnUnreapableProcess(t *testing.T) {
	logger := logmon.NewWriter(io.Discard)
	p, err := New(context.Background(), t.Name(), config.ModelConfig{Cmd: "irrelevant"}, logger, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.postKillGrace = 30 * time.Millisecond

	cmdDone := make(chan struct{}) // deliberately never closed
	cancelled := false
	cancel := func() { cancelled = true }

	done := make(chan struct{})
	start := time.Now()
	go func() {
		p.killProcess(nil, cancel, cmdDone, 5*time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
		elapsed := time.Since(start)
		if elapsed > time.Second {
			t.Errorf("killProcess took %v to give up — expected it to return promptly after its bounded grace period", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("killProcess blocked indefinitely waiting for an OS process that will never be reaped — the exact z4 wedge-recovery regression")
	}
	if !cancelled {
		t.Error("killProcess must always call cancel(), even when giving up on cmdDone")
	}
}

func TestShouldRecoverWedge(t *testing.T) {
	cases := []struct {
		name        string
		hardCtxErr  error
		clientErr   error
		inflight    int64
		wantRecover bool
	}{
		{
			name:        "our own timeout fires with requests piled up behind --parallel 1 (the z4 regression)",
			hardCtxErr:  context.DeadlineExceeded,
			clientErr:   context.DeadlineExceeded,
			inflight:    5, // retries/concurrent sessions queued — must NOT block recovery
			wantRecover: true,
		},
		{
			name:        "our own timeout fires with no other requests queued",
			hardCtxErr:  context.DeadlineExceeded,
			clientErr:   context.DeadlineExceeded,
			inflight:    1,
			wantRecover: true,
		},
		{
			name:        "client disconnect, sole in-flight request — recover",
			hardCtxErr:  nil,
			clientErr:   context.Canceled,
			inflight:    1,
			wantRecover: true,
		},
		{
			name:        "client disconnect while others are queued — do not disrupt them",
			hardCtxErr:  nil,
			clientErr:   context.Canceled,
			inflight:    3,
			wantRecover: false,
		},
		{
			name:        "client disconnect cancels the shared parent context too — hardCtxErr is Canceled, not DeadlineExceeded",
			hardCtxErr:  context.Canceled,
			clientErr:   context.Canceled,
			inflight:    1,
			wantRecover: true, // falls through to the client-disconnect branch
		},
		{
			name:        "request completed normally — no recovery",
			hardCtxErr:  nil,
			clientErr:   nil,
			inflight:    1,
			wantRecover: false,
		},
	}
	for _, c := range cases {
		if got := shouldRecoverWedge(c.hardCtxErr, c.clientErr, c.inflight); got != c.wantRecover {
			t.Errorf("%s: shouldRecoverWedge = %v, want %v", c.name, got, c.wantRecover)
		}
	}
}

func TestParallelFromCmd(t *testing.T) {
	cases := []struct {
		cmd  string
		want int
		ok   bool
	}{
		{"llama-server --parallel 4", 4, true},
		{"llama-server -np 2 --model m.gguf", 2, true},
		{"llama-server --parallel=8", 8, true},
		{"llama-server -np=3", 3, true},
		{"llama-server --model m.gguf", 0, false}, // absent → keep default
		{"llama-server --parallel 0", 0, false},   // non-positive ignored
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := parallelFromCmd(c.cmd)
		if got != c.want || ok != c.ok {
			t.Errorf("parallelFromCmd(%q) = (%d,%v), want (%d,%v)", c.cmd, got, ok, c.want, c.ok)
		}
	}
}

func TestNew_SerializeSlotCapacity(t *testing.T) {
	logger := logmon.NewWriter(io.Discard)
	cases := []struct {
		name    string
		conf    config.ModelConfig
		wantCap int
		wantNil bool
	}{
		{"mlx serializes to 1", config.ModelConfig{Backend: config.BackendMLX}, 1, false},
		{"llamacpp --parallel 3", config.ModelConfig{Cmd: "llama-server --parallel 3"}, 3, false},
		{"llamacpp --parallel 1 serializes", config.ModelConfig{Cmd: "llama-server --parallel 1"}, 1, false},
		{"llamacpp without --parallel is unbounded", config.ModelConfig{Cmd: "llama-server --model m.gguf"}, 0, true},
	}
	for _, c := range cases {
		ctx, cancel := context.WithCancel(context.Background())
		p, err := New(ctx, c.name, c.conf, logger, logger)
		if err != nil {
			cancel()
			t.Fatalf("%s: New: %v", c.name, err)
		}
		switch {
		case c.wantNil && p.serializeSlot != nil:
			t.Errorf("%s: expected nil serializeSlot (unbounded), got cap %d", c.name, cap(p.serializeSlot))
		case !c.wantNil && cap(p.serializeSlot) != c.wantCap:
			t.Errorf("%s: serializeSlot cap = %d, want %d", c.name, cap(p.serializeSlot), c.wantCap)
		}
		cancel()
	}
}
