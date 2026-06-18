package server

import (
	"testing"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/event"
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

// readyOne = one ready (unloadable) model present.
const readyOne = 1

func TestMemGuard_TriggersAfterConsecutiveLowSamples(t *testing.T) {
	g := &memGuardState{conf: memGuardConf()}
	now := time.Now()

	// 36 GB host, 2.5 GB available = ~7% — below the 10% threshold
	if g.Observe(2500, 36000, readyOne, now) {
		t.Fatal("should not trigger on the first low sample")
	}
	if !g.Observe(2500, 36000, readyOne, now.Add(5*time.Second)) {
		t.Fatal("should trigger on the second consecutive low sample")
	}
}

func TestMemGuard_HealthySampleResetsCounter(t *testing.T) {
	g := &memGuardState{conf: memGuardConf()}
	now := time.Now()

	if g.Observe(2500, 36000, readyOne, now) {
		t.Fatal("should not trigger on the first low sample")
	}
	// recovery above threshold resets the consecutive counter
	if g.Observe(12000, 36000, readyOne, now.Add(5*time.Second)) {
		t.Fatal("healthy sample must not trigger")
	}
	if g.Observe(2500, 36000, readyOne, now.Add(10*time.Second)) {
		t.Fatal("counter should have reset; one low sample must not trigger")
	}
}

func TestMemGuard_CooldownSuppressesRetrigger(t *testing.T) {
	g := &memGuardState{conf: memGuardConf()}
	now := time.Now()

	g.Observe(2500, 36000, readyOne, now)
	if !g.Observe(2500, 36000, readyOne, now.Add(5*time.Second)) {
		t.Fatal("expected initial trigger")
	}

	// still low immediately after: suppressed by cooldown
	g.Observe(2500, 36000, readyOne, now.Add(10*time.Second))
	if g.Observe(2500, 36000, readyOne, now.Add(15*time.Second)) {
		t.Fatal("must not re-trigger within cooldown")
	}

	// under sustained pressure, the first sample after cooldown expiry
	// re-triggers immediately (the consecutive count is already met)
	if !g.Observe(2500, 36000, readyOne, now.Add(70*time.Second)) {
		t.Fatal("expected re-trigger after cooldown")
	}
}

// TestMemGuard_DoesNotUnloadLoadingModel is the regression guard for the
// macOS misfire: when no model is ready to unload (the only model is still
// loading, unloadable=0), sustained low memory must NOT trigger — and must
// not consume the cooldown, so a real trigger can still fire once a model
// becomes ready.
func TestMemGuard_DoesNotUnloadLoadingModel(t *testing.T) {
	g := &memGuardState{conf: memGuardConf()}
	now := time.Now()

	// Sustained low memory while a model is loading (unloadable=0).
	if g.Observe(2500, 36000, 0, now) {
		t.Fatal("must not trigger while loading")
	}
	if g.Observe(2500, 36000, 0, now.Add(5*time.Second)) {
		t.Fatal("must not trigger while loading even after consecutive samples")
	}
	// Model finishes loading and becomes ready; still under pressure. The
	// cooldown was never consumed, so this fires immediately.
	if !g.Observe(2500, 36000, 1, now.Add(10*time.Second)) {
		t.Fatal("should trigger once a ready model exists under sustained pressure")
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

	if g.Observe(0, 0, readyOne, now) || g.Observe(-1, 36000, readyOne, now) {
		t.Fatal("invalid samples must never trigger")
	}
}
