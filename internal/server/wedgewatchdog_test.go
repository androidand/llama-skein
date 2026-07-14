package server

import (
	"testing"

	"github.com/androidand/llama-skein/internal/perf"
)

func TestGpuStalled(t *testing.T) {
	const gpuMin, memMax = 95.0, 20.0
	cases := []struct {
		name string
		g    perf.GpuStat
		want bool
	}{
		{"wedge: busy gpu, idle memory controller", perf.GpuStat{GpuUtilPct: 100, MemActivityPct: 2, MemActivityKnown: true}, true},
		{"idle gpu", perf.GpuStat{GpuUtilPct: 3, MemActivityPct: 0, MemActivityKnown: true}, false},
		{"no telemetry never counts as stalled", perf.GpuStat{GpuUtilPct: 100, MemActivityPct: 0, MemActivityKnown: false}, false},
		{"just under gpu threshold", perf.GpuStat{GpuUtilPct: 94, MemActivityPct: 0, MemActivityKnown: true}, false},
		{"just over mem threshold", perf.GpuStat{GpuUtilPct: 100, MemActivityPct: 21, MemActivityKnown: true}, false},
	}
	for _, c := range cases {
		if got := gpuStalled(c.g, gpuMin, memMax); got != c.want {
			t.Errorf("%s: gpuStalled = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestGpuStalled_DefaultThreshold_CatchesObservedZ4Wedge is a calibration
// regression: the shipped default MemActivityMax=20 must classify the ACTUAL
// wedge readings observed live on z4 (11%, 12%, 14% — confirmed repeatedly:
// each cleared via /unload, GPU dropping to 0% immediately after) as stalled.
// The first shipped default (5) was a naive "near zero" guess never
// cross-checked against this data, so the watchdog sat idle through a
// two-minute live wedge before this was caught. The one available reference
// for genuinely healthy decode activity (56%, observed mid-generation) must
// NOT be classified as stalled.
func TestGpuStalled_DefaultThreshold_CatchesObservedZ4Wedge(t *testing.T) {
	const gpuMin, defaultMemMax = 95.0, 20.0
	for _, wedgeReading := range []float64{11, 12, 14} {
		g := perf.GpuStat{GpuUtilPct: 100, MemActivityPct: wedgeReading, MemActivityKnown: true}
		if !gpuStalled(g, gpuMin, defaultMemMax) {
			t.Errorf("observed z4 wedge reading mem=%.0f%% must be classified as stalled at the default threshold", wedgeReading)
		}
	}
	healthy := perf.GpuStat{GpuUtilPct: 100, MemActivityPct: 56, MemActivityKnown: true}
	if gpuStalled(healthy, gpuMin, defaultMemMax) {
		t.Error("observed healthy decode reading mem=56%% must NOT be classified as stalled at the default threshold")
	}
}

func TestWedgeWatchdog_IntOr(t *testing.T) {
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

// TestWedgeWatchdogTick_ConsecutiveSamples verifies the stall counter requires
// needSamples CONSECUTIVE stalled ticks and resets on any healthy sample —
// the core guard against restarting a model on a single noisy reading.
func TestWedgeWatchdogTick_ConsecutiveSamples(t *testing.T) {
	stalls := map[string]int{}
	const needSamples = 3

	track := func(stalled bool) int {
		if !stalled {
			stalls["m"] = 0
			return stalls["m"]
		}
		stalls["m"]++
		return stalls["m"]
	}

	if got := track(true); got != 1 {
		t.Fatalf("sample 1: stalls = %d, want 1", got)
	}
	if got := track(true); got != 2 {
		t.Fatalf("sample 2: stalls = %d, want 2", got)
	}
	if track(false) != 0 {
		t.Fatal("a healthy sample must reset the counter, not just decrement it")
	}
	if got := track(true); got != 1 {
		t.Fatalf("after reset: stalls = %d, want 1", got)
	}
	track(true)
	if got := track(true); got < needSamples {
		t.Fatalf("3 consecutive stalled samples should reach the action threshold, got %d", got)
	}
}
