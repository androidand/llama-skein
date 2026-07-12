package proxy

import (
	"testing"

	"github.com/androidand/llama-skein/internal/perf"
)

func TestProxyManager_ParallelFromCmd(t *testing.T) {
	cases := []struct {
		cmd    string
		want   int
		wantOk bool
	}{
		{"llama-server --model m.gguf --parallel 4", 4, true},
		{"llama-server -np 2 --model m.gguf", 2, true},
		{"llama-server --parallel=8", 8, true},
		{"llama-server -np=3", 3, true},
		{"llama-server --model m.gguf", 0, false}, // absent → not specified
		{"llama-server --parallel abc", 0, false}, // unparseable → not specified
		{"llama-server --parallel 0", 0, false},   // non-positive ignored
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := parallelFromCmd(c.cmd)
		if got != c.want || ok != c.wantOk {
			t.Errorf("parallelFromCmd(%q) = (%d,%v), want (%d,%v)", c.cmd, got, ok, c.want, c.wantOk)
		}
	}
}

func TestProxyManager_GpuStalled(t *testing.T) {
	const gpuMin, memMax = 95.0, 5.0
	cases := []struct {
		name string
		g    perf.GpuStat
		want bool
	}{
		{"wedge: busy gpu, idle memory controller", perf.GpuStat{GpuUtilPct: 100, MemActivityPct: 2, MemActivityKnown: true}, true},
		{"healthy decode: busy gpu, busy memory", perf.GpuStat{GpuUtilPct: 100, MemActivityPct: 60, MemActivityKnown: true}, false},
		{"idle gpu", perf.GpuStat{GpuUtilPct: 3, MemActivityPct: 0, MemActivityKnown: true}, false},
		{"no telemetry never counts as stalled", perf.GpuStat{GpuUtilPct: 100, MemActivityPct: 0, MemActivityKnown: false}, false},
		{"just under gpu threshold", perf.GpuStat{GpuUtilPct: 94, MemActivityPct: 0, MemActivityKnown: true}, false},
	}
	for _, c := range cases {
		if got := gpuStalled(c.g, gpuMin, memMax); got != c.want {
			t.Errorf("%s: gpuStalled = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestProxyManager_IntOr(t *testing.T) {
	if intOr(0, 60) != 60 {
		t.Error("zero should fall back to default")
	}
	if intOr(-1, 60) != 60 {
		t.Error("negative should fall back to default")
	}
	if intOr(10, 60) != 10 {
		t.Error("positive value should win")
	}
}
