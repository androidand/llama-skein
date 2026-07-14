package server

import (
	"time"

	"github.com/androidand/llama-skein/internal/perf"
	"github.com/androidand/llama-skein/internal/process"
)

// startWedgeWatchdog launches the GPU-stall watchdog: it periodically checks
// every running llama.cpp model for the wedge signature — GPU utilization
// pinned high while GPU memory-controller activity stays near zero, the
// signature of a stuck compute kernel rather than real work — and restarts
// the backend when it persists, REGARDLESS of whether any HTTP request is
// currently in flight.
//
// This is deliberately independent of the request-scoped recovery in
// internal/process (maxRequestTimeSecs + cancelBusySlots): that recovery only
// runs as part of a specific request's own lifecycle (its timeout expiring,
// or its context being cancelled), so a wedge that forms after the triggering
// request already returned — or with no request in flight at all — would
// otherwise sit unrecovered until some future request happens to hit the same
// stuck slot and waits out its own timeout, which can be many minutes. This
// watchdog closes that gap by watching the GPU directly.
//
// It is a no-op without a perf monitor or when the host does not expose
// exactly one GPU with measured memory-activity telemetry (mem_activity_known)
// — a metric-less platform must never be treated as stalled. Disable via
// `wedgeWatchdog: {enabled: false}`.
func (s *Server) startWedgeWatchdog() {
	wd := s.cfg.WedgeWatchdog
	if wd.Enabled != nil && !*wd.Enabled {
		return
	}
	if s.perf == nil {
		return
	}
	grace := time.Duration(intOr(wd.GraceSecs, 60)) * time.Second
	interval := time.Duration(intOr(wd.IntervalSecs, 10)) * time.Second
	needSamples := intOr(wd.Samples, 3)
	gpuMin := float64(intOr(wd.GpuBusyThreshold, 95))
	memMax := float64(intOr(wd.MemActivityMax, 5))

	go func() {
		select {
		case <-s.shutdownCtx.Done():
			return
		case <-time.After(grace):
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		stalls := map[string]int{} // model ID → consecutive stalled samples
		for {
			select {
			case <-s.shutdownCtx.Done():
				return
			case <-ticker.C:
				s.wedgeWatchdogTick(gpuMin, memMax, needSamples, stalls)
			}
		}
	}()
}

func (s *Server) wedgeWatchdogTick(gpuMin, memMax float64, needSamples int, stalls map[string]int) {
	_, gpus := s.perf.Current()
	// Require exactly one GPU so a stall is unambiguously attributable to it.
	if len(gpus) != 1 {
		return
	}
	stalled := gpuStalled(gpus[0], gpuMin, memMax)

	running := map[string]bool{}
	for id, st := range s.local.RunningModels() {
		if st != process.StateReady {
			continue
		}
		mc, ok := s.cfg.Models[id]
		if !ok || !mc.IsLlamaCpp() {
			continue
		}
		running[id] = true
		if !stalled {
			stalls[id] = 0
			continue
		}
		stalls[id]++
		if stalls[id] >= needSamples {
			s.proxylog.Warnf("<%s> wedge watchdog: GPU %.0f%% busy / %.0f%% mem-activity persisted for %d samples with no progress — restarting wedged backend", id, gpus[0].GpuUtilPct, gpus[0].MemActivityPct, stalls[id])
			delete(stalls, id)
			go s.local.Unload(30*time.Second, id)
		}
	}
	for id := range stalls {
		if !running[id] {
			delete(stalls, id)
		}
	}
}

// gpuStalled reports the wedge signature: the GPU is pinned busy while its
// memory controller is idle. Requires measured memory-activity telemetry
// (MemActivityKnown) so a platform that never reports it is never treated as
// stalled.
func gpuStalled(g perf.GpuStat, gpuMin, memMax float64) bool {
	return g.MemActivityKnown && g.GpuUtilPct >= gpuMin && g.MemActivityPct <= memMax
}

// intOr returns v when positive, else fallback.
func intOr(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}
