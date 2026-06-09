package config

import (
	"fmt"
	"runtime"
)

// MemoryGuardConfig protects the host from memory exhaustion. When available
// system memory stays below MinAvailablePct for ConsecutiveSamples checks in
// a row, all local models are unloaded. On macOS, exhausting unified memory
// while a model holds wired GPU allocations can panic the whole machine
// (destroying all in-flight work), so the guard defaults to enabled there;
// on other platforms the OOM killer makes exhaustion survivable and the
// guard defaults to disabled.
type MemoryGuardConfig struct {
	// Enabled overrides the platform default (true on darwin, false
	// elsewhere) when set.
	Enabled *bool `yaml:"enabled"`

	// MinAvailablePct is the trigger threshold: available system memory
	// (including reclaimable cache) as a percentage of total. Default 10.
	MinAvailablePct float64 `yaml:"minAvailablePct"`

	// ConsecutiveSamples is how many consecutive below-threshold checks
	// (one every CheckIntervalSeconds) are required to trigger. Default 2.
	ConsecutiveSamples int `yaml:"consecutiveSamples"`

	// CheckIntervalSeconds is how often memory is sampled. Default 5.
	CheckIntervalSeconds int `yaml:"checkInterval"`

	// CooldownSeconds is the minimum time between two triggers. Default 60.
	CooldownSeconds int `yaml:"cooldown"`
}

// IsEnabled resolves the explicit setting or the platform default.
func (m MemoryGuardConfig) IsEnabled() bool {
	if m.Enabled != nil {
		return *m.Enabled
	}
	return runtime.GOOS == "darwin"
}

// Normalize fills zero values with defaults and rejects nonsensical
// settings. Callers that construct Config directly (tests, embedding) get
// the same defaults as YAML loading by calling this before use.
func (m MemoryGuardConfig) Normalize() (MemoryGuardConfig, error) {
	if m.MinAvailablePct == 0 {
		m.MinAvailablePct = 10
	}
	if m.MinAvailablePct < 0 || m.MinAvailablePct > 90 {
		return m, fmt.Errorf("memoryGuard.minAvailablePct must be between 0 and 90, got %v", m.MinAvailablePct)
	}
	if m.ConsecutiveSamples == 0 {
		m.ConsecutiveSamples = 2
	}
	if m.ConsecutiveSamples < 1 {
		return m, fmt.Errorf("memoryGuard.consecutiveSamples must be >= 1, got %d", m.ConsecutiveSamples)
	}
	if m.CheckIntervalSeconds == 0 {
		m.CheckIntervalSeconds = 5
	}
	if m.CheckIntervalSeconds < 1 {
		return m, fmt.Errorf("memoryGuard.checkInterval must be >= 1 second, got %d", m.CheckIntervalSeconds)
	}
	if m.CooldownSeconds == 0 {
		m.CooldownSeconds = 60
	}
	if m.CooldownSeconds < 0 {
		return m, fmt.Errorf("memoryGuard.cooldown must be >= 0 seconds, got %d", m.CooldownSeconds)
	}
	return m, nil
}
