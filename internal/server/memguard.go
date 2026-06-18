package server

import (
	"fmt"
	"runtime"
	"sort"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/event"
	"github.com/androidand/llama-skein/internal/perf"
	"github.com/androidand/llama-skein/internal/process"
	"github.com/androidand/llama-skein/internal/shared"
)

// macOS kern.memorystatus_vm_pressure_level values (perf.SysStat.MemPressureLevel).
const (
	pressureWarning  = 2
	pressureCritical = 4
)

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
		// macOS kernel verdict: 1 normal, 2 warning, 4 critical.
		critical = st.MemPressureLevel >= pressureCritical
		pressured = st.MemPressureLevel >= pressureWarning
		return pressured, critical, fmt.Sprintf("kernel pressure level %d", st.MemPressureLevel)
	}
	if st.MemTotalMB <= 0 || st.MemAvailableMB < 0 {
		return false, false, ""
	}
	pct := float64(st.MemAvailableMB) / float64(st.MemTotalMB) * 100
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
		signal = "kernel pressure level >= warning"
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
				AvailableMB:    st.MemAvailableMB,
				TotalMB:        st.MemTotalMB,
				ThresholdPct:   mg.MinAvailablePct,
				UnloadedModels: ready,
			})
			s.local.Unload(5*time.Second, ready...)
			s.proxylog.Infof("memory guard: unload complete; %d model(s) freed, reload on next request (cooldown %ds)", len(ready), mg.CooldownSeconds)
		}
	}()
}
