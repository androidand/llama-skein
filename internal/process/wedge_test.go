package process

import (
	"context"
	"io"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/logmon"
)

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
