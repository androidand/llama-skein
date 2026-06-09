package server

import (
	"testing"
	"time"

	"github.com/androidand/llama-skein/internal/config"
)

func memGuardConf() config.MemoryGuardConfig {
	return config.MemoryGuardConfig{
		MinAvailablePct:      10,
		ConsecutiveSamples:   2,
		CheckIntervalSeconds: 5,
		CooldownSeconds:      60,
	}
}

func TestMemGuard_TriggersAfterConsecutiveLowSamples(t *testing.T) {
	g := &memGuardState{conf: memGuardConf()}
	now := time.Now()

	// 36 GB host, 2.5 GB available = ~7% — below the 10% threshold
	if g.Observe(2500, 36000, now) {
		t.Fatal("should not trigger on the first low sample")
	}
	if !g.Observe(2500, 36000, now.Add(5*time.Second)) {
		t.Fatal("should trigger on the second consecutive low sample")
	}
}

func TestMemGuard_HealthySampleResetsCounter(t *testing.T) {
	g := &memGuardState{conf: memGuardConf()}
	now := time.Now()

	if g.Observe(2500, 36000, now) {
		t.Fatal("should not trigger on the first low sample")
	}
	// recovery above threshold resets the consecutive counter
	if g.Observe(12000, 36000, now.Add(5*time.Second)) {
		t.Fatal("healthy sample must not trigger")
	}
	if g.Observe(2500, 36000, now.Add(10*time.Second)) {
		t.Fatal("counter should have reset; one low sample must not trigger")
	}
}

func TestMemGuard_CooldownSuppressesRetrigger(t *testing.T) {
	g := &memGuardState{conf: memGuardConf()}
	now := time.Now()

	g.Observe(2500, 36000, now)
	if !g.Observe(2500, 36000, now.Add(5*time.Second)) {
		t.Fatal("expected initial trigger")
	}

	// still low immediately after: suppressed by cooldown
	g.Observe(2500, 36000, now.Add(10*time.Second))
	if g.Observe(2500, 36000, now.Add(15*time.Second)) {
		t.Fatal("must not re-trigger within cooldown")
	}

	// under sustained pressure, the first sample after cooldown expiry
	// re-triggers immediately (the consecutive count is already met)
	if !g.Observe(2500, 36000, now.Add(70*time.Second)) {
		t.Fatal("expected re-trigger after cooldown")
	}
}

func TestMemGuard_IgnoresInvalidSamples(t *testing.T) {
	g := &memGuardState{conf: memGuardConf()}
	now := time.Now()

	if g.Observe(0, 0, now) || g.Observe(-1, 36000, now) {
		t.Fatal("invalid samples must never trigger")
	}
}
