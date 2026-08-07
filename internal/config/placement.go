package config

import "fmt"

// PlacementConfig is the policy block for automatic model placement: whether
// llama-skein may rewrite a model's launch flags (in memory, never persisted)
// to run models larger than VRAM in a hybrid GPU + system-RAM configuration,
// and how much memory it must leave untouched while doing so.
type PlacementConfig struct {
	// Mode selects the placement strategy for models without hand-pinned
	// placement flags:
	//   auto   — plan per model: full GPU when it fits, hybrid when it
	//            doesn't, refuse when even hybrid can't fit safely (default)
	//   gpu    — never rewrite flags; models keep today's behavior
	//   hybrid — like auto, but refuse rather than run a model full-GPU-only
	//            when hybrid is impossible (reserved for future use)
	//   cpu    — plan every model CPU-side (diagnostics)
	Mode string `yaml:"mode"`

	// HostReserveGiB is host RAM never allocated to CPU-resident weights.
	// 0 = default: max(12 GiB, 10% of effective total).
	HostReserveGiB int `yaml:"hostReserveGiB"`

	// GpuReserveGiB is VRAM the planner leaves free (also the --fit-target
	// handed to the engine's own fitting). 0 = default: max(2 GiB, 5% of VRAM).
	GpuReserveGiB int `yaml:"gpuReserveGiB"`

	// MinimumContext is the smallest --ctx-size automatic placement may plan
	// for (also handed to the engine as --fit-ctx). 0 = default 8192,
	// matching the fleet's ctx floor.
	MinimumContext int `yaml:"minimumContext"`

	// AllowKvQuantization permits the planner to quantize the KV cache to
	// make a model fit. Default false: KV quality is never traded silently.
	AllowKvQuantization bool `yaml:"allowKvQuantization"`

	// MaxRetries bounds how many progressively safer placements are tried
	// after a memory-class load failure before the model is given up on.
	// 0 = default 3. Set to a negative value to disable adaptive retry.
	MaxRetries int `yaml:"maxRetries"`
}

const (
	PlacementModeAuto   = "auto"
	PlacementModeGPU    = "gpu"
	PlacementModeHybrid = "hybrid"
	PlacementModeCPU    = "cpu"

	defaultPlacementMinCtx     = 8192
	defaultPlacementMaxRetries = 3
	gib                        = 1024 // MB per GiB
)

// EffectiveMode returns the configured mode with the default applied.
func (p PlacementConfig) EffectiveMode() string {
	if p.Mode == "" {
		return PlacementModeAuto
	}
	return p.Mode
}

// Validate rejects unknown modes and negative reserves.
func (p PlacementConfig) Validate() error {
	switch p.Mode {
	case "", PlacementModeAuto, PlacementModeGPU, PlacementModeHybrid, PlacementModeCPU:
	default:
		return fmt.Errorf("placement.mode must be one of auto|gpu|hybrid|cpu, got %q", p.Mode)
	}
	if p.HostReserveGiB < 0 || p.GpuReserveGiB < 0 || p.MinimumContext < 0 {
		return fmt.Errorf("placement reserves and minimumContext must not be negative")
	}
	return nil
}

// HostReserveMB is the host RAM reserve in MB for a host with the given
// effective total (MB): the configured value, else max(12 GiB, 10% of total).
func (p PlacementConfig) HostReserveMB(effectiveTotalMB int) int {
	if p.HostReserveGiB > 0 {
		return p.HostReserveGiB * gib
	}
	reserve := 12 * gib
	if pct := effectiveTotalMB / 10; pct > reserve {
		reserve = pct
	}
	return reserve
}

// GpuReserveMB is the VRAM reserve in MB for a card with the given total
// (MB): the configured value, else max(2 GiB, 5% of VRAM).
func (p PlacementConfig) GpuReserveMB(vramTotalMB int) int {
	if p.GpuReserveGiB > 0 {
		return p.GpuReserveGiB * gib
	}
	reserve := 2 * gib
	if pct := vramTotalMB / 20; pct > reserve {
		reserve = pct
	}
	return reserve
}

// MinCtx is the minimum plannable context with the default applied.
func (p PlacementConfig) MinCtx() int {
	if p.MinimumContext > 0 {
		return p.MinimumContext
	}
	return defaultPlacementMinCtx
}

// RetryBudget is the number of adaptive placement retries allowed after a
// memory-class failure: the configured value, else 3. A negative
// configured value disables adaptive retry entirely (0 attempts).
func (p PlacementConfig) RetryBudget() int {
	switch {
	case p.MaxRetries < 0:
		return 0
	case p.MaxRetries == 0:
		return defaultPlacementMaxRetries
	default:
		return p.MaxRetries
	}
}
