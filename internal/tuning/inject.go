package tuning

import (
	"runtime"
	"sort"
	"strings"

	"github.com/androidand/llama-skein/internal/config"
)

// ToOverride converts the config-level tuning block into an Override.
func ToOverride(tc *config.TuningConfig) Override {
	if tc == nil {
		return Override{}
	}
	return Override{
		Enabled:    tc.Enabled,
		FlashAttn:  tc.FlashAttn,
		Parallel:   tc.Parallel,
		MTP:        tc.MTP,
		ExtraArgs:  tc.ExtraArgs,
		GfxTarget:  tc.GfxTarget,
		BackendEnv: tc.BackendEnv,
	}
}

// tuningGloballyDisabled reports whether the operator set tuning.enabled=false,
// which turns off ALL auto-injection (flags and env alike).
func tuningGloballyDisabled(tc *config.TuningConfig) bool {
	return tc != nil && tc.Enabled != nil && !*tc.Enabled
}

// BackendEnvFor returns the glibc allocator env vars to inject into llama.cpp
// backends on this host, or nil when they should not apply: non-Linux, tuning
// globally disabled, the backend_env toggle set false, or an empty database.
// Returned map is a copy. Exposed for GET /api/tuning.
func (db *Database) BackendEnvFor(tc *config.TuningConfig) map[string]string {
	return db.backendEnvToInject(tc, runtime.GOOS == "linux")
}

// backendEnvToInject is BackendEnvFor with the Linux gate as a parameter, so
// the resolution logic is testable off-Linux.
func (db *Database) backendEnvToInject(tc *config.TuningConfig, isLinux bool) map[string]string {
	if !isLinux || tuningGloballyDisabled(tc) {
		return nil
	}
	if tc != nil && tc.BackendEnv != nil && !*tc.BackendEnv {
		return nil
	}
	if len(db.BackendEnv.LinuxGlibc) == 0 {
		return nil
	}
	out := make(map[string]string, len(db.BackendEnv.LinuxGlibc))
	for k, v := range db.BackendEnv.LinuxGlibc {
		out[k] = v
	}
	return out
}

// injectEnvDefaults appends KEY=VALUE for each default var whose key is not
// already present in env. A var the user set in the model's env always wins.
// Keys are added in sorted order for deterministic output. Returns the
// (possibly extended) slice and whether anything was added.
func injectEnvDefaults(env []string, defaults map[string]string) ([]string, bool) {
	present := make(map[string]bool, len(env))
	for _, e := range env {
		k, _, ok := strings.Cut(e, "=")
		if !ok {
			k = e
		}
		present[k] = true
	}
	keys := make([]string, 0, len(defaults))
	for k := range defaults {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	added := false
	for _, k := range keys {
		if present[k] {
			continue
		}
		env = append(env, k+"="+defaults[k])
		added = true
	}
	return env, added
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

// ApplyToConfig injects tuning into every llamacpp model: effective-profile
// flags into Cmd (preserving the pre-injection command in TuningOriginalCmd)
// and glibc allocator env caps into Env (on Linux). It mutates and returns
// cfg. Flags are applied only when a profile resolves; env is applied
// independently (it is glibc-generic, not gfx-specific). tuning.enabled=false
// disables both. Called at config load/reload (fork-owned main), so the proxy
// launch path needs no change — it reads the tuned Cmd and merged Env.
func (db *Database) ApplyToConfig(cfg config.Config, detectedGfx string) config.Config {
	if tuningGloballyDisabled(cfg.Tuning) {
		return cfg
	}
	eff, _, ok := db.EffectiveFor(detectedGfx, cfg.Tuning)
	applyFlags := ok && eff.Enabled
	envDefaults := db.BackendEnvFor(cfg.Tuning)
	if !applyFlags && len(envDefaults) == 0 {
		return cfg
	}
	for id, mc := range cfg.Models {
		if !isLlamaCPP(mc.Backend) {
			continue
		}
		changed := false
		if applyFlags {
			isMTP := IsMTPModel(mc.Cmd, metadataMTPEnabled(mc.Metadata))
			tuned := ApplyProfile(mc.Cmd, eff.Profile, isMTP, eff.ExtraArgs)
			if tuned != mc.Cmd {
				mc.TuningOriginalCmd = mc.Cmd
				mc.Cmd = tuned
				changed = true
			}
		}
		if len(envDefaults) > 0 {
			if env, added := injectEnvDefaults(mc.Env, envDefaults); added {
				mc.Env = env
				changed = true
			}
		}
		if changed {
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
