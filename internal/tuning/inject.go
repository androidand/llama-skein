package tuning

import (
	"github.com/androidand/llama-skein/internal/config"
)

// ToOverride converts the config-level tuning block into an Override.
func ToOverride(tc *config.TuningConfig) Override {
	if tc == nil {
		return Override{}
	}
	return Override{
		Enabled:   tc.Enabled,
		FlashAttn: tc.FlashAttn,
		Parallel:  tc.Parallel,
		MTP:       tc.MTP,
		ExtraArgs: tc.ExtraArgs,
		GfxTarget: tc.GfxTarget,
	}
}

// useCaseOf returns the configured use-case, or "" for the database default.
func useCaseOf(tc *config.TuningConfig) string {
	if tc == nil {
		return ""
	}
	return tc.UseCase
}

// EffectiveFor resolves the effective tuning for a host: it applies the
// config override (including GfxTarget override) to the built-in profile for
// the resolved gfx, and returns the Effective plus the gfx actually used.
// detectedGfx is what DetectGfx found ("" if none). Returns ok=false when no
// profile exists for the resolved gfx (nothing to inject).
func (db *Database) EffectiveFor(detectedGfx string, tc *config.TuningConfig) (eff Effective, gfx string, ok bool) {
	ovr := ToOverride(tc)
	gfx = detectedGfx
	if ovr.GfxTarget != "" {
		gfx = ovr.GfxTarget
	}
	base, found := db.Lookup(gfx, useCaseOf(tc))
	if !found {
		// No profile: still honor an override that only supplies extra_args or
		// forces flags, so the user isn't blocked on an unknown gfx.
		if hasAnyForcedFlag(ovr) {
			return Resolve(Profile{}, ovr), gfx, true
		}
		return Effective{Enabled: false}, gfx, false
	}
	return Resolve(base, ovr), gfx, true
}

func hasAnyForcedFlag(o Override) bool {
	return o.FlashAttn != nil || o.Parallel != nil || o.MTP != nil || len(o.ExtraArgs) > 0
}

// ApplyToConfig injects effective-profile flags into every llamacpp model's
// Cmd, preserving the pre-injection command in TuningOriginalCmd. It mutates
// and returns cfg. A no-op when tuning is disabled or no profile applies.
// This is called at config load/reload (fork-owned main), so the proxy launch
// path needs no change — it already reads the (now tuned) Cmd.
func (db *Database) ApplyToConfig(cfg config.Config, detectedGfx string) config.Config {
	eff, _, ok := db.EffectiveFor(detectedGfx, cfg.Tuning)
	if !ok || !eff.Enabled {
		return cfg
	}
	for id, mc := range cfg.Models {
		if !isLlamaCPP(mc.Backend) {
			continue
		}
		isMTP := IsMTPModel(mc.Cmd, metadataMTPEnabled(mc.Metadata))
		tuned := ApplyProfile(mc.Cmd, eff.Profile, isMTP, eff.ExtraArgs)
		if tuned != mc.Cmd {
			mc.TuningOriginalCmd = mc.Cmd
			mc.Cmd = tuned
			cfg.Models[id] = mc
		}
	}
	return cfg
}

func isLlamaCPP(backend string) bool {
	return backend == "" || backend == string(config.BackendLlamaCpp)
}

// metadataMTPEnabled reads metadata.mtp.enabled from a model's Metadata map.
func metadataMTPEnabled(md map[string]any) bool {
	if md == nil {
		return false
	}
	mtp, ok := md["mtp"].(map[string]any)
	if !ok {
		return false
	}
	enabled, _ := mtp["enabled"].(bool)
	return enabled
}
