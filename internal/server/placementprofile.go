package server

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/offload"
	"github.com/androidand/llama-skein/internal/perf"
	"github.com/androidand/llama-skein/internal/placement"
	"github.com/androidand/llama-skein/internal/process"
	"github.com/androidand/llama-skein/pkg/gguf"
)

// placementProfileInterval is how often the profiler checks whether a
// running model's placement is worth recording. Deliberately unhurried: a
// profile describes a steady state, and sampling right at load time would
// catch allocation still settling.
const placementProfileInterval = 30 * time.Second

// placementProfilePaths are the candidate locations for the learned-placement
// store, in preference order. A daemon started without HOME in its
// environment (the container entrypoint case on z4) resolves no home
// directory at all, which silently disabled learning entirely — so fall back
// to the conventional daemon state directory rather than giving up.
func placementProfilePaths(home string) []string {
	var paths []string
	if home != "" {
		paths = append(paths, filepath.Join(home, ".llama-skein", "skein", "placements.json"))
	}
	return append(paths, filepath.Join("/var/lib", "llama-skein", "placements.json"))
}

// placementProfileStore returns a store at the first candidate path whose
// directory can be created. nil (learning disabled) only when none can.
func placementProfileStore(home string) *placement.ProfileStore {
	for _, p := range placementProfilePaths(home) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
			return placement.NewProfileStore(p)
		}
	}
	return nil
}

// placementKey builds the identity a learned profile is valid for. ok=false
// when the model's weights or the host's memory cannot be read, in which
// case nothing is looked up or recorded.
func (s *Server) placementKey(mc config.ModelConfig, ctx int) (placement.Key, bool) {
	path := parseModelPath(mc.Cmd)
	if path == "" {
		return placement.Key{}, false
	}
	info, err := os.Stat(path)
	if err != nil {
		return placement.Key{}, false
	}
	// Key on the whole split set's size, not the shard we happen to point
	// at: shard 1 is a few MB of header, so keying on it would fail to
	// invalidate a profile when the shards holding the actual weights change.
	modelSize := info.Size()
	if split := gguf.InspectSplit(path); split.IsSplit && split.TotalBytes > 0 {
		modelSize = split.TotalBytes
	}
	vramTotal, _ := s.vramMB()
	if vramTotal <= 0 {
		return placement.Key{}, false
	}
	_, hostTotal := s.hostMemMB()
	engine := ""
	if args, err := mc.SanitizedCommand(); err == nil && len(args) > 0 {
		engine = engineIdentity(args[0])
	}
	return placement.Key{
		ModelPath: path, ModelSize: modelSize, ModelMtime: info.ModTime().Unix(),
		Engine: engine, VRAMTotal: vramTotal, HostTotal: hostTotal, Ctx: ctx,
	}, true
}

// engineIdentity identifies the engine build a profile was measured against,
// so an upgraded llama-server invalidates profiles measured on the old one
// (its memory behaviour is exactly what changes between builds). Uses the
// binary's size and mtime — cheap, and it moves whenever the binary does.
func engineIdentity(binary string) string {
	info, err := os.Stat(binary)
	if err != nil {
		return binary
	}
	return binary + ":" + time.Unix(info.ModTime().Unix(), 0).UTC().Format(time.RFC3339) +
		":" + strconv.FormatInt(info.Size(), 10)
}

// planFromProfile converts a stored known-good profile back into a plan, so
// a proven placement is reused verbatim rather than re-derived (and
// re-risked) from an estimate.
func planFromProfile(p placement.Profile) placement.Plan {
	ops := make([]offload.FlagOp, 0, len(p.FlagOps))
	for _, f := range p.FlagOps {
		ops = append(ops, offload.FlagOp{Name: f.Name, Value: f.Value, Boolean: f.Boolean})
	}
	return placement.Plan{
		Mode:      p.Mode,
		FlagOps:   ops,
		NCpuMoe:   p.NCpuMoe,
		PerfClass: p.PerfClass,
		Confident: true,
		Estimate:  placement.Estimate{GPUMB: p.PeakVRAMMB, HostMB: p.PeakHostMB},
		Reason:    "reusing a placement previously measured as working on this host",
	}
}

// startPlacementProfiler records a known-good placement once a model has been
// running steadily. It measures rather than estimates: the value of a
// profile is that it says what the model ACTUALLY cost here, so the next
// launch can skip the estimate.
//
// A run is only learned when it left the configured reserves intact —
// see placement.WorthLearning. Learning a barely-survived run would make
// those margins the starting point for every future launch.
func (s *Server) startPlacementProfiler() {
	if s.placementProfiles == nil || s.perf == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(placementProfileInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.shutdownCtx.Done():
				return
			case <-ticker.C:
				s.recordReadyPlacements()
			}
		}
	}()
}

// recordReadyPlacements is one profiler pass over the ready models.
func (s *Server) recordReadyPlacements() {
	for id, state := range s.local.RunningModels() {
		if state != process.StateReady {
			continue
		}
		rec, planned := s.placements[id]
		if !planned || !rec.Plan.Applies() {
			continue // nothing we placed; nothing to learn
		}
		mc, ok := s.cfg.Models[id]
		if !ok {
			continue
		}
		ctx := 0
		if args, err := mc.SanitizedCommand(); err == nil {
			if v, ok := commandFlagInt(args, "--ctx-size", "-c"); ok {
				ctx = v
			}
		}
		key, ok := s.placementKey(mc, ctx)
		if !ok {
			continue
		}
		if _, exists := s.placementProfiles.Lookup(id, key); exists {
			continue // already learned for this exact world
		}

		peakVRAM, peakHost := s.currentUsageMB()
		profile, worth := placement.ProfileFrom(key, rec.Plan, peakVRAM, peakHost, 0,
			time.Now().Unix(), s.cfg.Placement)
		if !worth {
			continue
		}
		if err := s.placementProfiles.Record(id, profile); err != nil {
			s.proxylog.Warnf("placement: could not record profile for %q: %v", id, err)
			continue
		}
		s.proxylog.Infof("placement: recorded a known-good %s placement for %q (%d MB VRAM, %d MB host)",
			profile.Mode, id, peakVRAM, peakHost)
	}
}

// currentUsageMB samples what is resident right now: VRAM in use, and host
// memory in use within the effective (cgroup-aware) limit.
//
// The host figure UNDER-reports a hybrid placement's weights: llama.cpp
// mmaps them, so CPU-resident experts live in reclaimable page cache rather
// than anonymous memory and barely move "available". Treat the host number
// as a floor, not a measurement — which is why WorthLearning gates on the
// VRAM peak (real, and the constraint that actually bites) and only checks
// the host peak when one was observed.
func (s *Server) currentUsageMB() (vramMB, hostMB int) {
	if s.perf == nil {
		return 0, 0
	}
	sysStats, gpuStats := s.perf.Current()
	for _, g := range perf.LatestGPUs(gpuStats) {
		vramMB += g.MemUsedMB
	}
	if len(sysStats) > 0 {
		sys := sysStats[len(sysStats)-1]
		if total, avail := sys.EffectiveMemTotalMB(), sys.EffectiveMemAvailableMB(); total > 0 && avail >= 0 {
			hostMB = total - avail
		}
	}
	return vramMB, hostMB
}
