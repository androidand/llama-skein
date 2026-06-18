package server

import (
	"testing"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/event"
	"github.com/androidand/llama-skein/internal/perf"
	"github.com/androidand/llama-skein/internal/shared"
)

func memGuardConf() config.MemoryGuardConfig {
	return config.MemoryGuardConfig{
		MinAvailablePct:      10,
		ConsecutiveSamples:   2,
		CheckIntervalSeconds: 5,
		CooldownSeconds:      60,
	}
}

const (
	readyOne   = 1
	pressure   = true
	noPressure = false
	notCrit    = false
	crit       = true
)

func TestMemGuard_TriggersAfterConsecutiveWarnings(t *testing.T) {
	g := &memGuardState{conf: memGuardConf()}
	now := time.Now()

	// Warning-level pressure: needs ConsecutiveSamples (2) in a row.
	if g.Observe(pressure, notCrit, readyOne, now) {
		t.Fatal("should not trigger on the first warning sample")
	}
	if !g.Observe(pressure, notCrit, readyOne, now.Add(5*time.Second)) {
		t.Fatal("should trigger on the second consecutive warning sample")
	}
}

func TestMemGuard_CriticalFiresImmediately(t *testing.T) {
	g := &memGuardState{conf: memGuardConf()}
	// Critical pressure (jetsam imminent) bypasses the consecutive requirement.
	if !g.Observe(pressure, crit, readyOne, time.Now()) {
		t.Fatal("critical pressure must trigger on the first sample")
	}
}

func TestMemGuard_HealthySampleResetsCounter(t *testing.T) {
	g := &memGuardState{conf: memGuardConf()}
	now := time.Now()

	if g.Observe(pressure, notCrit, readyOne, now) {
		t.Fatal("should not trigger on the first warning sample")
	}
	// pressure clears → counter resets
	if g.Observe(noPressure, notCrit, readyOne, now.Add(5*time.Second)) {
		t.Fatal("no-pressure sample must not trigger")
	}
	if g.Observe(pressure, notCrit, readyOne, now.Add(10*time.Second)) {
		t.Fatal("counter should have reset; one warning sample must not trigger")
	}
}

func TestMemGuard_CooldownSuppressesRetrigger(t *testing.T) {
	g := &memGuardState{conf: memGuardConf()}
	now := time.Now()

	g.Observe(pressure, notCrit, readyOne, now)
	if !g.Observe(pressure, notCrit, readyOne, now.Add(5*time.Second)) {
		t.Fatal("expected initial trigger")
	}
	g.Observe(pressure, notCrit, readyOne, now.Add(10*time.Second))
	if g.Observe(pressure, notCrit, readyOne, now.Add(15*time.Second)) {
		t.Fatal("must not re-trigger within cooldown")
	}
	if !g.Observe(pressure, notCrit, readyOne, now.Add(70*time.Second)) {
		t.Fatal("expected re-trigger after cooldown")
	}
}

// TestMemGuard_DoesNotUnloadLoadingModel is the regression guard for the
// original macOS misfire: with no ready model to unload (unloadable=0, the
// only model is still loading), sustained pressure must NOT trigger — and must
// not consume the cooldown, so a real trigger still fires once a model is ready.
func TestMemGuard_DoesNotUnloadLoadingModel(t *testing.T) {
	g := &memGuardState{conf: memGuardConf()}
	now := time.Now()

	if g.Observe(pressure, notCrit, 0, now) {
		t.Fatal("must not trigger while loading")
	}
	if g.Observe(pressure, notCrit, 0, now.Add(5*time.Second)) {
		t.Fatal("must not trigger while loading even after consecutive samples")
	}
	if !g.Observe(pressure, notCrit, 1, now.Add(10*time.Second)) {
		t.Fatal("should trigger once a ready model exists under sustained pressure")
	}
}

// TestHostUnderPressure_MacOSUsesKernelLevel is the core fix: a resident large
// model drives available% low, but the kernel reports normal pressure, so the
// guard must NOT consider the host pressured.
func TestHostUnderPressure_MacOSUsesKernelLevel(t *testing.T) {
	// 4% available but kernel says normal (level 1): NOT pressured.
	healthy := perf.SysStat{MemTotalMB: 36864, MemAvailableMB: 1600, MemPressureLevel: 1}
	if p, _, _ := hostUnderPressure(healthy, 10); p {
		t.Errorf("kernel level 1 (normal) must not be treated as pressure despite low available%%")
	}
	// kernel warning (2): pressured, not critical.
	warn := perf.SysStat{MemTotalMB: 36864, MemAvailableMB: 1600, MemPressureLevel: 2}
	if p, c, _ := hostUnderPressure(warn, 10); !p || c {
		t.Errorf("kernel level 2 should be pressured+non-critical, got pressured=%v critical=%v", p, c)
	}
	// kernel critical (4): pressured + critical.
	critical := perf.SysStat{MemTotalMB: 36864, MemAvailableMB: 800, MemPressureLevel: 4}
	if p, c, _ := hostUnderPressure(critical, 10); !p || !c {
		t.Errorf("kernel level 4 should be pressured+critical, got pressured=%v critical=%v", p, c)
	}
}

// TestHostUnderPressure_FallbackUsesAvailablePct covers Linux/Windows, where no
// kernel pressure level is exposed (MemPressureLevel == 0).
func TestHostUnderPressure_FallbackUsesAvailablePct(t *testing.T) {
	low := perf.SysStat{MemTotalMB: 32000, MemAvailableMB: 1600} // 5% < 10%
	if p, _, _ := hostUnderPressure(low, 10); !p {
		t.Error("5% available should be pressured when threshold is 10%")
	}
	healthy := perf.SysStat{MemTotalMB: 32000, MemAvailableMB: 16000} // 50%
	if p, _, _ := hostUnderPressure(healthy, 10); p {
		t.Error("50% available must not be pressured")
	}
}

func TestMemGuard_TrippedEventEmitted(t *testing.T) {
	got := make(chan shared.MemoryGuardTrippedEvent, 1)
	off := event.On(func(e shared.MemoryGuardTrippedEvent) { got <- e })
	defer off()

	event.Emit(shared.MemoryGuardTrippedEvent{
		AvailableMB:    2500,
		TotalMB:        36000,
		ThresholdPct:   5,
		UnloadedModels: []string{"mlx-qwen3-35b-a3b"},
	})

	select {
	case e := <-got:
		if e.AvailableMB != 2500 || e.ThresholdPct != 5 ||
			len(e.UnloadedModels) != 1 || e.UnloadedModels[0] != "mlx-qwen3-35b-a3b" {
			t.Fatalf("unexpected event payload: %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected MemoryGuardTrippedEvent to be delivered to subscribers")
	}
}

func TestMemGuard_IgnoresInvalidSamples(t *testing.T) {
	g := &memGuardState{conf: memGuardConf()}
	now := time.Now()

	if g.Observe(noPressure, notCrit, readyOne, now) {
		t.Fatal("no-pressure sample must never trigger")
	}
}
