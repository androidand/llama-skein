package server

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/androidand/llama-skein/internal/perf"
	"github.com/androidand/llama-skein/internal/process"
)

// Fatal error patterns that indicate a wedged backend. These are matched
// against the last 20 lines of each process's stderr on every watchdog tick.
// The list is intentionally conservative — only patterns that are clearly
// fatal and leave the process alive but unable to make progress.
var fatalErrorPatterns = []string{
	"backend is in error state from a previous command buffer failure",
	"CommandBufferCallbackErrorOutOfMemory",
	"command buffer failed with error",
	"failed to compile metal shader",
}

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
		s.watchdogActive = false
		s.watchdogReason = "disabled by config"
		s.proxylog.Infof("GPU-stall watchdog inactive: disabled by config")
		return
	}
	if s.perf == nil {
		s.watchdogActive = false
		s.watchdogReason = "no perf monitor"
		s.proxylog.Infof("GPU-stall watchdog inactive: no perf monitor")
		return
	}
	_, gpus := s.perf.Current()
	if len(gpus) != 1 {
		s.watchdogActive = false
		s.watchdogReason = fmt.Sprintf("requires exactly 1 GPU, found %d", len(gpus))
		s.proxylog.Infof("GPU-stall watchdog inactive: requires exactly 1 GPU, found %d", len(gpus))
		return
	}
	if !gpus[0].MemActivityKnown {
		s.watchdogActive = false
		s.watchdogReason = "GPU does not report memory-activity telemetry"
		// NOTE: On macOS/Metal (darwin), this gate always fires because
		// internal/perf/monitor_unix.go is built with "//go:build unix && !darwin",
		// so MemActivityKnown is never true on Mac hosts. The GPU-stall watchdog
		// cannot detect wedges on macOS — a platform-independent fallback (e.g.,
		// stderr pattern matching) is needed for darwin coverage. See
		// harden-fleet-rollout, task 9.
		s.proxylog.Infof("GPU-stall watchdog inactive: GPU does not report memory-activity telemetry")
		return
	}

	s.watchdogActive = true
	s.watchdogReason = ""
	s.proxylog.Infof("GPU-stall watchdog active")

	grace := time.Duration(intOr(wd.GraceSecs, 60)) * time.Second
	interval := time.Duration(intOr(wd.IntervalSecs, 10)) * time.Second
	needSamples := intOr(wd.Samples, 3)
	gpuMin := float64(intOr(wd.GpuBusyThreshold, 95))
	memMax := float64(intOr(wd.MemActivityMax, 20))

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

		// Platform-independent: check stderr for fatal error patterns.
		// This works on macOS/Metal and any other platform where GPU
		// memory-activity telemetry is unavailable.
		if logger, ok := s.local.ProcessLogger(id); ok {
			if match := stderrFatalMatch(logger.GetHistory()); match != "" {
				s.proxylog.Warnf("<%s> wedge watchdog: fatal error pattern in stderr: %s — restarting wedged backend", id, match)
				go s.local.Unload(30*time.Second, id)
				continue
			}
		}

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

// stderrFatalMatch scans the last 20 lines of history for a fatal error
// pattern. Returns the matched pattern or "" if no match. This is a
// platform-independent fallback for detecting wedged backends when GPU
// memory-activity telemetry is unavailable (e.g., macOS/Metal).
func stderrFatalMatch(history []byte) string {
	if len(history) == 0 {
		return ""
	}
	lines := bytes.Split(bytes.TrimSpace(history), []byte("\n"))
	var last20 []byte
	n := 0
	for i := len(lines) - 1; i >= 0 && n < 20; i-- {
		trimmed := bytes.TrimSpace(lines[i])
		if len(trimmed) > 0 {
			last20 = append(last20, trimmed...)
			last20 = append(last20, '\n')
			n++
		}
	}
	if len(last20) == 0 {
		return ""
	}
	lower := bytes.ToLower(last20)
	for _, pat := range fatalErrorPatterns {
		if bytes.Contains(lower, []byte(strings.ToLower(pat))) {
			return pat
		}
	}
	return ""
}

// intOr returns v when positive, else fallback.
func intOr(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}
