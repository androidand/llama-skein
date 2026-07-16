package server

import (
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/fit"
	"github.com/androidand/llama-skein/internal/perf"
	"github.com/androidand/llama-skein/internal/router"
	"github.com/androidand/llama-skein/pkg/apicontract"
	"github.com/androidand/llama-skein/pkg/gguf"
)

// vramMB returns the host's total and free VRAM budget in MB for fit and
// recommendation math. See hostVRAM for the platform semantics.
func (s *Server) vramMB() (total, free int) {
	if s.perf == nil {
		return 0, 0
	}
	sysStats, gpuStats := s.perf.Current()
	unified := runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
	return hostVRAM(sysStats, gpuStats, unified, gpuWiredLimitMB())
}

// hostVRAM computes the VRAM budget from perf snapshots. Pure so every
// platform branch is unit-testable off-platform.
//
//   - Discrete GPUs: latest sample per GPU ID, summed across GPUs (llama.cpp
//     splits layers across cards by default).
//   - Unified memory (Apple Silicon): Metal will not wire the whole pool, so
//     total is capped at the wired limit (iogpu.wired_limit_mb, else ~70% of
//     RAM) and free at min(budget − GPU-used, MemAvailableMB) — a new model
//     competes with everything else resident, not just prior GPU allocations.
//   - No GPU: system RAM with MemAvailableMB as free (MemFreeMB is near zero
//     on macOS even at rest and must not be used as a budget).
func hostVRAM(sysStats []perf.SysStat, gpuStats []perf.GpuStat, unified bool, wiredLimitMB int) (total, free int) {
	if len(sysStats) == 0 {
		return 0, 0
	}
	sys := sysStats[len(sysStats)-1]
	availableMB := sys.MemAvailableMB
	if availableMB == 0 {
		availableMB = sys.MemFreeMB
	}

	gpus := perf.LatestGPUs(gpuStats)
	if len(gpus) == 0 {
		if unified {
			budget := unifiedBudgetMB(sys.MemTotalMB, wiredLimitMB)
			return budget, max0(min(budget, availableMB))
		}
		return sys.MemTotalMB, max0(availableMB)
	}

	var totalMB, usedMB int
	for _, g := range gpus {
		totalMB += g.MemTotalMB
		usedMB += g.MemUsedMB
	}
	if unified {
		// GpuStat totals on Apple report the whole unified pool; used is the
		// GPU-attributed slice (ioreg overlay).
		budget := unifiedBudgetMB(totalMB, wiredLimitMB)
		free = budget - usedMB
		if availableMB > 0 && availableMB < free {
			free = availableMB
		}
		return budget, max0(free)
	}
	return totalMB, max0(totalMB - usedMB)
}

// unifiedBudgetMB is the unified-memory ceiling Metal actually enforces: the
// wired limit when set, else ~70% of RAM (the OS default working set).
func unifiedBudgetMB(totalRAM, wiredLimitMB int) int {
	if wiredLimitMB > 0 {
		return wiredLimitMB
	}
	return totalRAM * 70 / 100
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

// commandFlagString returns the value of the first matching string flag in args.
func commandFlagString(args []string, names ...string) (string, bool) {
	for i, arg := range args {
		for _, name := range names {
			if arg == name && i+1 < len(args) {
				return args[i+1], true
			}
			if v, ok := strings.CutPrefix(arg, name+"="); ok {
				return v, true
			}
		}
	}
	return "", false
}

type cachedGGUF struct {
	mtime time.Time
	g     *gguf.GGUF
}

// parseGGUFCached is gguf.ParseFile behind a per-Server (path, mtime) cache.
// A weight file only changes when it is re-downloaded or replaced, which
// bumps mtime; everything else is repeat polls of an identical header.
func (s *Server) parseGGUFCached(path string) (*gguf.GGUF, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if v, ok := s.ggufCache.Load(path); ok {
		if c := v.(cachedGGUF); c.mtime.Equal(info.ModTime()) {
			return c.g, nil
		}
	}
	g, err := gguf.ParseFile(path)
	if err != nil {
		return nil, err
	}
	s.ggufCache.Store(path, cachedGGUF{mtime: info.ModTime(), g: g})
	return g, nil
}

// fitForModel computes the fit of one configured model to this host. ok is
// false only when the model ID is unknown.
func (s *Server) fitForModel(realName string) (apicontract.ModelFit, bool) {
	mc, ok := s.cfg.Models[realName]
	if !ok {
		return apicontract.ModelFit{}, false
	}
	backend := mc.Backend
	if backend == "" {
		backend = config.BackendLlamaCpp
	}
	mf := apicontract.ModelFit{
		Model:    realName,
		Backend:  apicontract.ModelFitBackend(backend),
		FitLevel: apicontract.No,
	}

	// MLX: weights are safetensors in the HF cache, not GGUF. Resolve the cache
	// snapshot from useModelName. vramMB already caps unified hosts at the GPU
	// wired limit (the ceiling MLX actually crashes at), not total RAM.
	if backend == config.BackendMLX {
		if mc.UseModelName == "" {
			mf.Reason = ptrOf("MLX fit needs useModelName to locate the model in the Hugging Face cache")
			return mf, true
		}
		shape, err := resolveMLXShape(mc.UseModelName)
		if err != nil {
			mf.Reason = ptrOf("could not read MLX model metadata: " + err.Error())
			return mf, true
		}
		p := fit.Params{} // MLX KV is f16 (no cache-type quantization)
		p.VRAMTotalMB, _ = s.vramMB()
		fillModelFit(&mf, fit.AnalyzeShape(shape, p))
		return mf, true
	}

	// Fit otherwise models GGUF (llama.cpp) memory. vLLM weights aren't modeled;
	// report unknown rather than a wrong number.
	ggufPath := parseModelPath(mc.Cmd)
	if backend != config.BackendLlamaCpp || ggufPath == "" {
		mf.Reason = ptrOf("fit estimate is currently computed for llamacpp GGUF and MLX safetensors models only")
		return mf, true
	}
	g, err := s.parseGGUFCached(ggufPath)
	if err != nil {
		mf.Reason = ptrOf("could not read GGUF metadata: " + err.Error())
		return mf, true
	}

	args, _ := mc.SanitizedCommand()
	p := fit.Params{}
	if kc, ok := commandFlagString(args, "--cache-type-k", "-ctk"); ok {
		p.KCacheBits = fit.BitsPerElement(kc)
	}
	if vc, ok := commandFlagString(args, "--cache-type-v", "-ctv"); ok {
		p.VCacheBits = fit.BitsPerElement(vc)
	}
	if v, ok := commandFlagInt(args, "--ctx-size", "-c"); ok {
		p.ConfiguredCtx = v
	}
	if v, ok := commandFlagInt(args, "--parallel", "-np"); ok {
		p.ParallelSlots = v
	}
	if v, ok := commandFlagInt(args, "--n-predict", "-n"); ok && v > 0 {
		p.OutputReserve = v
	}
	p.VRAMTotalMB, p.VRAMFreeMB = s.vramMB()
	if modelGetsWholeGPU(s.cfg, realName) {
		// This model belongs to an exclusive swap group: loading it evicts
		// every other model on the host (router.groupPlanner.EvictionFor), so
		// once resident it has the ENTIRE card — not whatever a co-resident
		// happens to be leaving free right this moment. Budgeting against
		// live free VRAM here made max_fit_ctx/under_configured swing with
		// whichever OTHER model was loaded at query time on any host running
		// several models in one exclusive group (z4: three models, one GPU),
		// which skein's context-fit sweep (a different repo, reacting to this
		// same value every cycle) then wrote straight into --ctx-size —
		// a persistent, real oscillation, not a transient glitch. 0 tells
		// fit.Analyze free VRAM is unknown here so it falls back to
		// VRAMTotalMB as the budget, matching what this model will actually
		// get once it's its turn.
		p.VRAMFreeMB = 0
	}

	fillModelFit(&mf, fit.Analyze(g, p))
	return mf, true
}

// modelGetsWholeGPU reports whether modelID will have the entire GPU to
// itself once it's the one running — i.e. its swap group is Exclusive, which
// evicts every other model on load — AND no other group is Persistent (a
// Persistent group's members survive even an Exclusive target's load, so the
// card would genuinely still be shared; fall back to live free VRAM then).
func modelGetsWholeGPU(cfg config.Config, modelID string) bool {
	var group string
	inGroup := false
	for gid, g := range cfg.Groups {
		for _, m := range g.Members {
			if m == modelID {
				group, inGroup = gid, true
			}
		}
	}
	if !inGroup || !cfg.Groups[group].Exclusive {
		return false
	}
	for gid, g := range cfg.Groups {
		if gid != group && g.Persistent {
			return false
		}
	}
	return true
}

// fillModelFit copies an engine Result onto the generated ModelFit DTO. Shared
// by the llama.cpp and MLX paths so both report identically.
func fillModelFit(mf *apicontract.ModelFit, r fit.Result) {
	mf.FitLevel = apicontract.ModelFitFitLevel(r.FitLevel)
	mf.MaxSafeCtx = r.MaxSafeCtx
	mf.ConfiguredCtx = ptrOf(r.ConfiguredCtx)
	mf.ModelMb = ptrOf(r.ModelMB)
	mf.KvMbAtMaxSafeCtx = ptrOf(r.KVMBAtMaxSafeCtx)
	mf.VramRequiredMb = ptrOf(r.VRAMRequiredMB)
	if r.VRAMTotalMB > 0 {
		mf.VramTotalMb = ptrOf(r.VRAMTotalMB)
	}
	if r.UnderConfigured {
		mf.UnderConfigured = ptrOf(true)
	}
	if r.MaxFitCtx > 0 {
		mf.MaxFitCtx = ptrOf(r.MaxFitCtx)
	}
	mf.Reason = ptrOf(r.Reason)
}

// handleAPIFitReport implements GET /api/fit — fit of every configured model
// to this host.
func (s *Server) handleAPIFitReport(w http.ResponseWriter, r *http.Request) {
	ids := make([]string, 0, len(s.cfg.Models))
	for id := range s.cfg.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	report := apicontract.FitReport{Models: make([]apicontract.ModelFit, 0, len(ids))}
	for _, id := range ids {
		if mf, ok := s.fitForModel(id); ok {
			report.Models = append(report.Models, mf)
		}
	}
	if total, free := s.vramMB(); total > 0 {
		report.VramTotalMb = ptrOf(total)
		report.VramFreeMb = ptrOf(free)
	}
	writeJSON(w, report)
}

// handleAPIModelFit implements GET /api/fit/{model}.
func (s *Server) handleAPIModelFit(w http.ResponseWriter, r *http.Request) {
	requested := strings.TrimPrefix(r.PathValue("model"), "/")
	realName, found := s.cfg.RealModelName(requested)
	if !found {
		router.SendResponse(w, r, http.StatusNotFound, "model not found")
		return
	}
	mf, ok := s.fitForModel(realName)
	if !ok {
		router.SendResponse(w, r, http.StatusNotFound, "model not found")
		return
	}
	writeJSON(w, mf)
}
