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

	// Fit currently models GGUF (llama.cpp) memory. MLX/vLLM weights aren't GGUF
	// and KV accounting differs; report unknown rather than a wrong number.
	ggufPath := parseModelPath(mc.Cmd)
	if backend != config.BackendLlamaCpp || ggufPath == "" {
		mf.Reason = ptrOf("fit estimate is currently computed for llamacpp GGUF models only")
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

	r := fit.Analyze(g, p)
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
	return mf, true
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
