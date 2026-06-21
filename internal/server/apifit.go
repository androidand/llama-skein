package server

import (
	"net/http"
	"sort"
	"strings"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/fit"
	"github.com/androidand/llama-skein/internal/router"
	"github.com/androidand/llama-skein/pkg/apicontract"
	"github.com/androidand/llama-skein/pkg/gguf"
)

// vramMB returns the host's total and free VRAM in MB (free is GPU VRAM, or
// system memory on unified/no-GPU hosts), mirroring freeVRAMBytes.
func (s *Server) vramMB() (total, free int) {
	if s.perf == nil {
		return 0, 0
	}
	sysStats, gpuStats := s.perf.Current()
	if len(sysStats) == 0 {
		return 0, 0
	}
	if len(gpuStats) > 0 {
		g := gpuStats[0]
		return g.MemTotalMB, max0(g.MemTotalMB - g.MemUsedMB)
	}
	sys := sysStats[len(sysStats)-1]
	// Unified memory: prefer available (reclaimable) over free for the budget.
	f := sys.MemAvailableMB
	if f == 0 {
		f = sys.MemFreeMB
	}
	return sys.MemTotalMB, max0(f)
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
	// snapshot from useModelName and budget against the GPU wired limit (the
	// ceiling MLX actually crashes at), not total RAM.
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
		total, _ := s.vramMB()
		p.VRAMTotalMB = mlxVRAMBudgetMB(total)
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
	g, err := gguf.ParseFile(ggufPath)
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
	if v, ok := commandFlagInt(args, "--n-predict", "-n"); ok && v > 0 {
		p.OutputReserve = v
	}
	p.VRAMTotalMB, p.VRAMFreeMB = s.vramMB()

	fillModelFit(&mf, fit.Analyze(g, p))
	return mf, true
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
	mf.Reason = ptrOf(r.Reason)
}

// mlxVRAMBudgetMB is the unified-memory budget the MLX fit scores against: the
// GPU wired limit when set, else the Apple-Silicon default working set (~70% of
// RAM). Using total RAM would overestimate the safe context and still let an
// OOM-crashing prompt through.
func mlxVRAMBudgetMB(totalRAM int) int {
	if wl := gpuWiredLimitMB(); wl > 0 {
		return wl
	}
	return totalRAM * 70 / 100
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
