package server

import (
	"sort"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/event"
	"github.com/androidand/llama-skein/internal/perf"
	"github.com/androidand/llama-skein/internal/process"
	"github.com/androidand/llama-skein/internal/shared"
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
// unload now. unloadable is the number of models that are safe to unload
// (StateReady) — a model still loading is excluded by the caller, since a load
// legitimately spikes memory and killing it is self-defeating.
//
// A trigger requires: available below threshold for ConsecutiveSamples in a
// row, at least one unloadable model, and the cooldown since the last actual
// trigger elapsed. When there is nothing to unload (only a loading model, or
// pressure from other processes) the guard does NOT fire and does NOT consume
// the cooldown — it keeps watching.
func (g *memGuardState) Observe(availableMB, totalMB, unloadable int, now time.Time) bool {
	if totalMB <= 0 || availableMB < 0 {
		g.consecutive = 0
		return false
	}
	pct := float64(availableMB) / float64(totalMB) * 100
	if pct >= g.conf.MinAvailablePct {
		g.consecutive = 0
		return false
	}
	g.consecutive++
	if g.consecutive < g.conf.ConsecutiveSamples {
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

	s.proxylog.Infof("memory guard: enabled (unload all models below %.0f%% available for %d checks, %ds interval, %ds cooldown)",
		mg.MinAvailablePct, mg.ConsecutiveSamples, mg.CheckIntervalSeconds, mg.CooldownSeconds)

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
			if err != nil || st.MemAvailableMB <= 0 || st.MemTotalMB <= 0 {
				continue // sampling unavailable on this platform; stay quiet
			}

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

			pct := float64(st.MemAvailableMB) / float64(st.MemTotalMB) * 100
			if state.consecutive == 0 && pct < mg.MinAvailablePct {
				s.proxylog.Warnf("memory guard: available memory low: %d/%d MB (%.1f%%, threshold %.0f%%); ready=%d loading=%d",
					st.MemAvailableMB, st.MemTotalMB, pct, mg.MinAvailablePct, len(ready), loading)
			}

			if !state.Observe(st.MemAvailableMB, st.MemTotalMB, len(ready), time.Now()) {
				continue
			}

			sort.Strings(ready)
			s.proxylog.Errorf("memory guard: available memory %d/%d MB below %.0f%% — unloading %d ready model(s) to prevent host memory exhaustion: %v",
				st.MemAvailableMB, st.MemTotalMB, mg.MinAvailablePct, len(ready), ready)
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
