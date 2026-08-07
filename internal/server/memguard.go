package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"time"

	"github.com/androidand/llama-skein/internal/chain"
	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/event"
	"github.com/androidand/llama-skein/internal/perf"
	"github.com/androidand/llama-skein/internal/placement"
	"github.com/androidand/llama-skein/internal/process"
	"github.com/androidand/llama-skein/internal/router"
	"github.com/androidand/llama-skein/internal/shared"
)

// macOS kern.memorystatus_vm_pressure_level critical value
// (perf.SysStat.MemPressureLevel: 1 normal, 2 warning, 4 critical). Only
// critical triggers the guard — see hostUnderPressure.
const pressureCritical = 4

// memGuardState is the pure decision core of the memory guard, kept free of
// goroutines and clocks so it can be unit-tested. Observe returns true when
// the caller should unload all local models: available memory has been below
// the threshold for the configured number of consecutive samples and the
// cooldown since the previous trigger has expired.
type memGuardState struct {
	conf        config.MemoryGuardConfig
	consecutive int
	lastTrigger time.Time
}

// Observe records one memory sample and reports whether the guard should
// unload now. The caller decides, per platform, whether the host is under
// pressure (see hostUnderPressure): on macOS this is the kernel's
// memorystatus level, not a raw available-% figure — a resident large model
// drives available% low without the system being in any danger.
//
// unloadable is the number of models safe to unload (StateReady); a model
// still loading is excluded by the caller, since a load legitimately spikes
// memory and killing it is self-defeating.
//
// A normal (warning-level) trigger requires sustained pressure for
// ConsecutiveSamples; critical pressure fires on the first sample (jetsam is
// imminent, there's no time to wait). Both require at least one unloadable
// model and an elapsed cooldown. When there is nothing to unload the guard
// does NOT fire and does NOT consume the cooldown — it keeps watching.
func (g *memGuardState) Observe(pressured, critical bool, unloadable int, now time.Time) bool {
	if !pressured {
		g.consecutive = 0
		return false
	}
	g.consecutive++
	needed := g.conf.ConsecutiveSamples
	if critical {
		needed = 1
	}
	if g.consecutive < needed {
		return false
	}
	if unloadable == 0 {
		return false
	}
	if !g.lastTrigger.IsZero() && now.Sub(g.lastTrigger) < time.Duration(g.conf.CooldownSeconds)*time.Second {
		return false
	}
	g.lastTrigger = now
	g.consecutive = 0
	return true
}

// hostUnderPressure translates one sample into (pressured, critical) using the
// best signal the platform offers. macOS exposes the kernel's holistic verdict
// (MemPressureLevel); everywhere else we fall back to the available-memory
// percentage, which is reliable on Linux. The reason string is for logging.
func hostUnderPressure(st perf.SysStat, minAvailablePct float64) (pressured, critical bool, reason string) {
	if st.MemPressureLevel > 0 {
		// macOS kernel verdict: 1 normal, 2 warning, 4 critical. Only CRITICAL
		// triggers an unload: warning is the normal steady state for a host whose
		// job is to hold a model that uses most of unified memory (the kernel
		// routinely asks for cache back and compresses fine). Unloading at warning
		// caused a load→ready→unload→reload cycle that made MLX models unusable.
		critical = st.MemPressureLevel >= pressureCritical
		pressured = critical
		return pressured, critical, fmt.Sprintf("kernel pressure level %d", st.MemPressureLevel)
	}
	// Pressure evaluates against the effective (cgroup-clamped) figures: a
	// container at its limit is about to be OOM-killed no matter how much
	// memory the host as a whole still has.
	totalMB, availableMB := st.EffectiveMemTotalMB(), st.EffectiveMemAvailableMB()
	if totalMB <= 0 || availableMB < 0 {
		return false, false, ""
	}
	pct := float64(availableMB) / float64(totalMB) * 100
	pressured = pct < minAvailablePct
	critical = pct < minAvailablePct/2
	return pressured, critical, fmt.Sprintf("available %.1f%% (threshold %.0f%%)", pct, minAvailablePct)
}

// startMemoryGuard launches the host memory-pressure guard. When available
// system memory stays below the configured threshold, every local model is
// unloaded — wired GPU memory is by far the largest allocation llama-skein
// controls, and on macOS releasing it is the difference between a recovered
// host and a kernel panic that destroys all in-flight work. Models reload on
// the next request once pressure clears.
//
// The guard samples memory itself (perf.ReadSysStats) rather than
// subscribing to the perf monitor, so it keeps working when performance
// monitoring is disabled in config.
func (s *Server) startMemoryGuard() {
	mg, err := s.cfg.MemoryGuard.Normalize()
	if err != nil {
		s.proxylog.Errorf("memory guard: invalid config, guard disabled: %v", err)
		return
	}
	if !mg.IsEnabled() {
		return
	}

	signal := fmt.Sprintf("available < %.0f%%", mg.MinAvailablePct)
	if runtime.GOOS == "darwin" {
		signal = "kernel pressure level critical"
	}
	s.proxylog.Infof("memory guard: enabled (unload ready models on sustained pressure [%s] for %d checks, %ds interval, %ds cooldown)",
		signal, mg.ConsecutiveSamples, mg.CheckIntervalSeconds, mg.CooldownSeconds)

	go func() {
		state := &memGuardState{conf: mg}
		ticker := time.NewTicker(time.Duration(mg.CheckIntervalSeconds) * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-s.shutdownCtx.Done():
				return
			case <-ticker.C:
			}

			st, err := perf.ReadSysStats()
			if err != nil {
				continue // sampling unavailable on this platform; stay quiet
			}

			pressured, critical, reason := hostUnderPressure(st, mg.MinAvailablePct)

			// Only StateReady models are safe to unload. A model still loading
			// (StateStarting/warmup) legitimately spikes memory; unloading it
			// would kill the very load that tripped the guard — the misfire
			// that made the guard unusable on macOS.
			ready := make([]string, 0)
			loading := 0
			for id, pst := range s.local.RunningModels() {
				switch pst {
				case process.StateReady:
					ready = append(ready, id)
				case process.StateStarting:
					loading++
				}
			}

			if state.consecutive == 0 && pressured {
				s.proxylog.Warnf("memory guard: host under memory pressure (%s); ready=%d loading=%d",
					reason, len(ready), loading)
			}

			if !state.Observe(pressured, critical, len(ready), time.Now()) {
				continue
			}

			sort.Strings(ready)
			s.proxylog.Errorf("memory guard: sustained memory pressure (%s) — unloading %d ready model(s) to prevent host memory exhaustion: %v",
				reason, len(ready), ready)
			// Surface a structured error to clients (UI/skein) so models don't
			// just silently vanish — the log line alone is easy to miss.
			event.Emit(shared.MemoryGuardTrippedEvent{
				AvailableMB:    st.EffectiveMemAvailableMB(),
				TotalMB:        st.EffectiveMemTotalMB(),
				ThresholdPct:   mg.MinAvailablePct,
				UnloadedModels: ready,
			})
			s.local.Unload(5*time.Second, ready...)
			s.proxylog.Infof("memory guard: unload complete; %d model(s) freed, reload on next request (cooldown %ds)", len(ready), mg.CooldownSeconds)
		}
	}()
}

// CreateMemoryPressureGateMiddleware refuses, with a fast retryable 503, to
// START loading a model that is not yet resident while the host is already
// under CRITICAL memory pressure. Without this gate a load can spend minutes
// fighting other processes for CPU/disk on a contended host, finally
// succeed, and then be evicted by startMemoryGuard's background loop within
// seconds of becoming ready — wasted work that is, to the caller,
// indistinguishable from "this model will never load" (m3 incident,
// 2026-08-05: the host stayed critically pressured from unrelated concurrent
// processes across several load attempts, each ending the same way). A fast,
// clear error lets the caller (opencode/skein) back off or fall back to
// another host immediately instead of blocking for minutes.
//
// It only acts on a model that is not already resident — a model already
// ready serves the request without spawning anything, so host pressure is
// irrelevant to it and must not fail it. Fails open on every uncertain
// signal: the guard disabled/misconfigured, a sampling error, or a reading
// short of CRITICAL (the same bar startMemoryGuard uses to evict, chosen
// because macOS sits at "warning" as a normal steady state — see
// hostUnderPressure).
func (s *Server) CreateMemoryPressureGateMiddleware() chain.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, err := router.FetchContext(r, s.cfg)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			if _, loaded := s.modelState(data.ModelID); loaded {
				next.ServeHTTP(w, r) // already resident: no load about to happen, host pressure is irrelevant
				return
			}
			mg, mgErr := s.cfg.MemoryGuard.Normalize()
			st, statsErr := s.readSysStats()

			reason, refuse := memoryPressureRefusal(mg, mgErr, st, statsErr)
			if !refuse && statsErr == nil {
				// Placement admission: a hybrid plan sized against the memory
				// free at planning time can be stale by the time the model is
				// actually requested (another model resident, an unrelated
				// process grown). Refuse a load whose planned host-RAM
				// footprint no longer fits what's available now — before the
				// weights start streaming into RAM — rather than discovering
				// it via the OOM killer. Transient, so 503/retryable.
				plan, planned := s.placementFor(data.ModelID)
				reason, refuse = placementAdmissionRefusal(plan, planned, st)
			}
			if !refuse {
				next.ServeHTTP(w, r)
				return
			}
			s.proxylog.Warnf("<%s> memory-pressure-gate: refusing to start load — host under critical memory pressure (%s)", data.ModelID, reason)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusServiceUnavailable) // 503, retryable
			_ = json.NewEncoder(w).Encode(hostMemoryPressureError(data.ModelID, reason))
		})
	}
}

// memoryPressureRefusal is the pure decision core of the pressure gate,
// isolated from the HTTP plumbing and the two IO calls (config.Normalize,
// perf.ReadSysStats) that feed it, so it can be unit-tested without a real
// host. Callers must have already confirmed the target model is not
// resident — see CreateMemoryPressureGateMiddleware, which skips both IO
// calls entirely for an already-loaded model. Refuses only when the guard is
// enabled and correctly configured, sampling succeeded, and
// hostUnderPressure reports CRITICAL. Fails open on every other combination.
func memoryPressureRefusal(mg config.MemoryGuardConfig, mgErr error, st perf.SysStat, statsErr error) (reason string, refuse bool) {
	if mgErr != nil || !mg.IsEnabled() || statsErr != nil {
		return "", false
	}
	_, critical, reason := hostUnderPressure(st, mg.MinAvailablePct)
	return reason, critical
}

// placementFor returns the planned placement for a model, if one was made.
func (s *Server) placementFor(id string) (plan placement.Plan, ok bool) {
	realName, found := s.cfg.RealModelName(id)
	if !found {
		realName = id
	}
	rec, ok := s.placements[realName]
	return rec.Plan, ok
}

// placementAdmissionRefusal refuses a load whose planned host-RAM footprint
// exceeds the memory effectively available right now. Pure, so the decision
// is testable without a host.
//
// Only host-resident weights are checked: a full-GPU plan puts nothing in
// system RAM, and the VRAM side is already gated by the fit guard (507) at
// plan time. Fails open on everything uncertain — no plan, an estimate of
// zero, or unreadable memory figures — because refusing a load we cannot
// size is worse than attempting it.
func placementAdmissionRefusal(plan placement.Plan, ok bool, st perf.SysStat) (reason string, refuse bool) {
	if !ok || !plan.Confident || plan.Estimate.HostMB <= 0 {
		return "", false
	}
	availableMB := st.EffectiveMemAvailableMB()
	if availableMB <= 0 {
		return "", false
	}
	if plan.Estimate.HostMB <= availableMB {
		return "", false
	}
	return fmt.Sprintf("planned host-memory footprint %d MB exceeds the %d MB available now (placement: %s)",
		plan.Estimate.HostMB, availableMB, plan.Mode), true
}

// readSysStats samples current system memory stats, or returns
// sysStatsOverride when a test has set one — production code always takes
// the perf.ReadSysStats() path.
func (s *Server) readSysStats() (perf.SysStat, error) {
	if s.sysStatsOverride != nil {
		return s.sysStatsOverride()
	}
	return perf.ReadSysStats()
}

func hostMemoryPressureError(model, reason string) any {
	type errBody struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	}
	return struct {
		Error errBody `json:"error"`
	}{Error: errBody{
		Message: fmt.Sprintf("model %q was not loaded: host is under critical memory pressure (%s); retry shortly", model, reason),
		Type:    "host_memory_pressure_error",
		Code:    "host_memory_critical",
	}}
}
