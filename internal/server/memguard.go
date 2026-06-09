package server

import (
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/perf"
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

func (g *memGuardState) Observe(availableMB, totalMB int, now time.Time) bool {
	if totalMB <= 0 || availableMB < 0 {
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
			if err != nil || st.MemAvailableMB == 0 {
				continue // sampling unavailable on this platform; stay quiet
			}

			if state.consecutive == 0 && float64(st.MemAvailableMB)/float64(st.MemTotalMB)*100 < mg.MinAvailablePct {
				s.proxylog.Warnf("memory guard: available memory low: %d/%d MB (%.1f%%, threshold %.0f%%)",
					st.MemAvailableMB, st.MemTotalMB,
					float64(st.MemAvailableMB)/float64(st.MemTotalMB)*100, mg.MinAvailablePct)
			}

			if !state.Observe(st.MemAvailableMB, st.MemTotalMB, time.Now()) {
				continue
			}

			running := s.local.RunningModels()
			if len(running) == 0 {
				s.proxylog.Warnf("memory guard: memory critically low (%d MB available) but no local models are loaded — pressure is from other processes",
					st.MemAvailableMB)
				continue
			}

			models := make([]string, 0, len(running))
			for id := range running {
				models = append(models, id)
			}
			s.proxylog.Errorf("memory guard: available memory %d/%d MB below %.0f%% — unloading all local models to prevent host memory exhaustion: %v",
				st.MemAvailableMB, st.MemTotalMB, mg.MinAvailablePct, models)
			s.local.Unload(5 * time.Second)
			s.proxylog.Infof("memory guard: unload complete; models reload on next request (cooldown %ds)", mg.CooldownSeconds)
		}
	}()
}
