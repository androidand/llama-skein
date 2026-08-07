package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/fit"
	"github.com/androidand/llama-skein/internal/logmon"
	"github.com/androidand/llama-skein/internal/placement"
)

// placementRecord is what applyAutoPlacement decided for one model: the plan,
// the command as configured (before any rewrite), and — when the engine ships
// a llama-fit-params utility — the engine's own fitted arguments as a
// preflight cross-check.
type placementRecord struct {
	Plan        placement.Plan
	OriginalCmd string
	// AppliedCmd is the command the model will actually launch with once the
	// plan is in force. Everything that JUDGES a model — the fit report, the
	// fit guard, the prompt guard — must read this rather than the
	// configured command, or it scores a placement that is not what runs and
	// refuses a model the placement just rescued.
	AppliedCmd string
	// EffectiveArgs is llama-fit-params' stdout for the planned command
	// (empty when the tool is unavailable or failed — preflight is advisory,
	// never load-bearing).
	EffectiveArgs string
	// WeightBytes is the model's total weight size (the whole set for a
	// split GGUF), used to size the load deadline.
	WeightBytes int64
}

// raiseLoadDeadline lifts a model's health-check timeout to what its size
// and placement actually need, never lowering one the operator set higher.
// Without this a large model is killed mid-load and reported as a failure,
// which is what happened repeatedly to a 91 GB model on z4 under the 120 s
// default.
func raiseLoadDeadline(configured int, weightBytes int64, mode placement.Mode, id string, log *logmon.Monitor) int {
	need := placement.LoadDeadlineSeconds(weightBytes, mode)
	if need <= configured {
		return configured
	}
	log.Infof("placement: model %q needs longer to load than the configured %ds (%.0f GB, %s placement) — raising its health-check deadline to %ds",
		id, configured, float64(weightBytes)/(1<<30), mode, need)
	return need
}

// pinnedPlacementFlags are the arguments whose presence hands placement
// wholly to the operator: upstream llama.cpp disables its own fitting
// per-argument on exactly these, and automatic placement follows the same
// contract (tuning-injection precedent: explicit flags always win).
var pinnedPlacementFlags = []string{
	"--n-gpu-layers", "-ngl", "--gpu-layers",
	"--n-cpu-moe", "-ncmoe",
	"--cpu-moe", "-cmoe",
	"--override-tensor", "-ot",
	"--tensor-split", "-ts",
}

func hasPinnedPlacement(args []string) bool {
	for _, a := range args {
		flag, _, _ := strings.Cut(a, "=")
		for _, p := range pinnedPlacementFlags {
			if flag == p {
				return true
			}
		}
	}
	return false
}

// applyAutoPlacement computes a placement plan for every llama.cpp model and
// applies hybrid/CPU plans to the in-memory command (never persisted to the
// config file — the original is kept in the placement record, and a model
// whose plan is "gpu" is left byte-for-byte untouched, which is also what
// makes "revert to normal" automatic for small models). Confident refusals
// go to s.unfittable so the load gate and preload refuse them instead of
// OOM-ing the host. Everything else fails open.
//
// Must run after s.cfg and s.perf are set, before clampModelsToFit (the
// clamp should judge the rewritten command) and before the router captures
// per-model configs.
func (s *Server) applyAutoPlacement() {
	if s.placements == nil {
		s.placements = map[string]placementRecord{}
	}
	if s.placementRetries == nil {
		s.placementRetries = newPlacementRetry()
	}
	if s.unfittable == nil {
		s.unfittable = map[string]string{}
	}
	if s.cfg.Placement.EffectiveMode() == config.PlacementModeGPU {
		return // never rewrite anything in gpu mode
	}
	for id, mc := range s.cfg.Models {
		rec, ok := s.planModel(id, mc)
		if !ok {
			continue
		}
		s.applyPlanned(id, rec)
	}
	s.warnSlowPlacementTimeouts()
}

// warnSlowPlacementTimeouts flags request-time caps that a legitimately slow
// hybrid model will trip. A cpu-bound-hybrid decode is paced by host memory
// bandwidth and can take many times longer than the same model would on the
// card — a timeout tuned for GPU serving reads that as a hang and kills a
// working model. Warn rather than silently raising the cap: the operator
// chose that number, and placement must not quietly override it.
func (s *Server) warnSlowPlacementTimeouts() {
	for id, rec := range s.placements {
		if rec.Plan.PerfClass != placement.PerfCPUBoundHybrid && rec.Plan.PerfClass != placement.PerfCPUOnly {
			continue
		}
		cap := s.cfg.MaxRequestTimeSecs
		if mc, ok := s.cfg.Models[id]; ok && mc.MaxRequestTimeSecs > 0 {
			cap = mc.MaxRequestTimeSecs
		}
		if cap > 0 && cap < slowPlacementTimeoutAdviceSecs {
			s.proxylog.Warnf("placement: model %q runs %s — generation is paced by host memory bandwidth and may take minutes per response, but maxRequestTimeSecs is %ds. Raise it for this model or requests will be cut off mid-generation.",
				id, rec.Plan.PerfClass, cap)
		}
	}
}

// slowPlacementTimeoutAdviceSecs is the request-time cap below which a
// host-bandwidth-paced placement is likely to be cut off mid-generation.
// Advisory only — it triggers a log line, never a config change.
const slowPlacementTimeoutAdviceSecs = 600

// applyPlanned records one model's placement decision and enacts it: hybrid/
// CPU plans rewrite the in-memory command, confident refusals mark the model
// unfittable, everything else is a no-op record. Split from the planning loop
// so the enactment rules are testable with synthetic plans.
func (s *Server) applyPlanned(id string, rec placementRecord) {
	s.placements[id] = rec
	plan := rec.Plan

	// Size the load deadline for EVERY model we could size, not just the
	// ones we rewrite: a 40 GB full-GPU model also needs longer than 120 s to
	// page in. Weight size is known here even when the plan is "unknown"
	// (VRAM telemetry still warming up at boot), so this is the one placement
	// decision that can always be made this early — and doing it here, before
	// the router captures configs, keeps it race-free.
	if mc, ok := s.cfg.Models[id]; ok && rec.WeightBytes > 0 {
		if raised := raiseLoadDeadline(mc.HealthCheckTimeout, rec.WeightBytes, plan.Mode, id, s.proxylog); raised != mc.HealthCheckTimeout {
			mc.HealthCheckTimeout = raised
			s.cfg.Models[id] = mc
		}
	}

	switch {
	case plan.Mode == placement.ModeRefuse && plan.Confident:
		s.unfittable[id] = plan.Reason
		s.proxylog.Warnf("placement: model %q cannot be placed within safe memory budgets — it will be refused rather than loaded. %s", id, plan.Reason)
	case plan.Applies():
		mc, ok := s.cfg.Models[id]
		if !ok {
			return
		}
		newCmd, err := applyFlagOps(mc.Cmd, plan.FlagOps)
		if err != nil {
			s.proxylog.Warnf("placement: model %q plan not applied (cmd rewrite failed): %v", id, err)
			return
		}
		mc.Cmd = newCmd
		s.cfg.Models[id] = mc
		rec.AppliedCmd = newCmd
		s.placements[id] = rec
		s.proxylog.Infof("placement: model %q planned %s (%s): %s", id, plan.Mode, plan.PerfClass, plan.Reason)
		if out, ok := s.preflightFitParams(mc); ok {
			rec.EffectiveArgs = out
			s.placements[id] = rec
		}
	}
}

// planModel computes the placement plan for one model. ok=false means the
// model is out of scope (non-llamacpp backend, no GGUF path, unreadable
// metadata) and no record is kept.
func (s *Server) planModel(id string, mc config.ModelConfig) (placementRecord, bool) {
	in, ok := s.planInputs(id, mc)
	if !ok {
		return placementRecord{}, false
	}
	// A placement previously measured as working on this exact host, model
	// file, engine build and context beats a fresh estimate: it is the same
	// question already answered by reality. Invalidated automatically when
	// any of those inputs change (placement.Key).
	if !in.PinnedPlacement {
		if key, ok := s.placementKey(mc, in.ConfiguredCtx); ok {
			if profile, found := s.placementProfiles.Lookup(id, key); found {
				s.proxylog.Infof("placement: model %q reusing a known-good %s placement measured on this host", id, profile.Mode)
				return placementRecord{Plan: planFromProfile(profile), OriginalCmd: mc.Cmd, WeightBytes: in.Shape.WeightBytes}, true
			}
		}
	}
	return placementRecord{Plan: placement.Compute(in), OriginalCmd: mc.Cmd, WeightBytes: in.Shape.WeightBytes}, true
}

// planInputs builds the planner inputs for a model from its command and the
// host's current memory picture. Shared by the boot-time plan and the
// adaptive retry, so a retry plans against the same rules — but against
// memory as it is at retry time.
func (s *Server) planInputs(id string, mc config.ModelConfig) (placement.Inputs, bool) {
	if mc.Backend != "" && mc.Backend != config.BackendLlamaCpp {
		return placement.Inputs{}, false
	}
	ggufPath := parseModelPath(mc.Cmd)
	if ggufPath == "" {
		return placement.Inputs{}, false
	}
	g, err := s.parseGGUFCached(ggufPath)
	if err != nil {
		return placement.Inputs{}, false
	}
	if len(g.SplitMissing) > 0 {
		// An incomplete split set cannot load at all, and its summed size
		// understates the real model — planning against it would produce a
		// confident, wrong answer. Decline instead.
		s.proxylog.Warnf("placement: model %q is an incomplete split GGUF (%d shard(s) missing, first: %s) — not planning a placement for it",
			id, len(g.SplitMissing), g.SplitMissing[0])
		return placement.Inputs{}, false
	}
	args, err := mc.SanitizedCommand()
	if err != nil {
		return placement.Inputs{}, false
	}

	in := placement.Inputs{
		Shape:           fit.ShapeFromGGUF(g),
		PinnedPlacement: hasPinnedPlacement(args),
		Policy:          s.cfg.Placement,
	}
	if v, ok := commandFlagInt(args, "--ctx-size", "-c"); ok {
		in.ConfiguredCtx = v
	}
	if v, ok := commandFlagInt(args, "--parallel", "-np"); ok {
		in.ParallelSlots = v
	}
	if kc, ok := commandFlagString(args, "--cache-type-k", "-ctk"); ok {
		in.KCacheBits = fit.BitsPerElement(kc)
	}
	if vc, ok := commandFlagString(args, "--cache-type-v", "-ctv"); ok {
		in.VCacheBits = fit.BitsPerElement(vc)
	}

	// VRAM budget: what this model gets once resident. Exclusive-group
	// models get the whole card (same reasoning as fitForModel); shared
	// models get what is free right now.
	total, free := s.vramMB()
	in.VRAMBudgetMB = free
	if modelGetsWholeGPU(s.cfg, id) || free <= 0 {
		in.VRAMBudgetMB = total
	}

	// Host budget inputs: cgroup-effective figures; the planner applies the
	// policy reserve itself.
	if s.perf != nil {
		if sysStats, _ := s.perf.Current(); len(sysStats) > 0 {
			sys := sysStats[len(sysStats)-1]
			in.HostTotalMB = sys.EffectiveMemTotalMB()
			in.HostAvailableMB = sys.EffectiveMemAvailableMB()
		}
	}

	return in, true
}

// preflightFitParams cross-checks a planned command against the engine's own
// allocator: llama-fit-params (shipped beside llama-server in current
// llama.cpp releases) prints the fitted arguments for a command without
// loading the model. Advisory only — a missing or failing tool never blocks
// a load; the output is stored for API/UI visibility.
func (s *Server) preflightFitParams(mc config.ModelConfig) (string, bool) {
	args, err := mc.SanitizedCommand()
	if err != nil || len(args) == 0 {
		return "", false
	}
	tool := fitParamsPath(args[0])
	if tool == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, args[1:]...)
	cmd.Env = append(os.Environ(), mc.Env...)
	out, err := cmd.Output()
	if err != nil {
		s.proxylog.Warnf("placement: llama-fit-params preflight failed (advisory only): %v", err)
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// fitParamsPath locates llama-fit-params next to the engine binary. Empty
// when absent or not executable (older engine builds).
func fitParamsPath(engineBinary string) string {
	dir := filepath.Dir(engineBinary)
	if dir == "." {
		// Engine resolved via PATH; try PATH for the tool too.
		if p, err := exec.LookPath("llama-fit-params"); err == nil {
			return p
		}
		return ""
	}
	p := filepath.Join(dir, "llama-fit-params")
	if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
		return p
	}
	return ""
}
