package server

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/androidand/llama-skein/internal/fit"
	"github.com/androidand/llama-skein/internal/placement"
	"github.com/androidand/llama-skein/internal/router"
	"github.com/androidand/llama-skein/pkg/apicontract"
)

// handleAPIHypotheticalFit implements POST /api/fit/hypothetical — fit of a
// model that is NOT on disk (a gallery/catalog candidate), scored per quant
// variant against this host. Skein's fleet gallery calls this on every host
// to build its model × host × quant matrix before anything is downloaded.
func (s *Server) handleAPIHypotheticalFit(w http.ResponseWriter, r *http.Request) {
	var req apicontract.HypotheticalFitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		router.SendResponse(w, r, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	backend := apicontract.HypotheticalFitRequestBackendLlamacpp
	if req.Backend != nil {
		backend = *req.Backend
	}
	if !backend.Valid() {
		router.SendResponse(w, r, http.StatusBadRequest, "unsupported backend: hypothetical fit models llamacpp and mlx only")
		return
	}
	if len(req.Variants) == 0 {
		router.SendResponse(w, r, http.StatusBadRequest, "at least one variant (name + file_bytes) is required")
		return
	}
	for _, v := range req.Variants {
		if v.Name == "" || v.FileBytes <= 0 {
			router.SendResponse(w, r, http.StatusBadRequest, "every variant needs a name and a positive file_bytes")
			return
		}
	}

	d := fit.Descriptor{}
	if req.ParamsB != nil {
		d.ParamsB = float64(*req.ParamsB)
	}
	if req.ArchFamily != nil {
		d.ArchFamily = *req.ArchFamily
	}
	if dims := req.Dims; dims != nil {
		d.LayerCount = z64(dims.LayerCount)
		d.EmbeddingLength = z64(dims.EmbeddingLength)
		d.HeadCount = z64(dims.HeadCount)
		d.HeadCountKV = z64(dims.HeadCountKv)
		d.HeadDim = z64(dims.HeadDim)
		d.TrainedCtx = z64(dims.TrainedCtx)
	}

	p := fit.Params{Unproven: true}
	if req.RequestedCtx != nil && *req.RequestedCtx > 0 {
		p.ConfiguredCtx = *req.RequestedCtx
	}
	// MLX KV is f16 (no cache-type quantization); zero bits = engine default.
	if backend == apicontract.HypotheticalFitRequestBackendLlamacpp {
		if req.CacheTypeK != nil {
			p.KCacheBits = fit.BitsPerElement(*req.CacheTypeK)
		}
		if req.CacheTypeV != nil {
			p.VCacheBits = fit.BitsPerElement(*req.CacheTypeV)
		}
	}
	// Budget against total VRAM, not the live free figure: a gallery candidate
	// would swap in like any exclusive-group model (the modelGetsWholeGPU
	// rationale) — free VRAM at query time only reflects whichever model
	// happens to be resident right now.
	p.VRAMTotalMB, _ = s.vramMB()

	// Largest-first: a bigger quant of the same model is higher fidelity, so
	// the recommendation walk below returns the best variant that fits well.
	variants := make([]apicontract.HypotheticalQuantVariant, len(req.Variants))
	copy(variants, req.Variants)
	sort.Slice(variants, func(i, j int) bool { return variants[i].FileBytes > variants[j].FileBytes })

	// Host budget for hybrid feasibility (a variant larger than VRAM can
	// still be loadable): planner-gated, cgroup-effective, reserve-applied.
	hostAvailMB, hostTotalMB := s.hostMemMB()

	resp := apicontract.HypotheticalFitResponse{
		Backend:  apicontract.HypotheticalFitResponseBackend(backend),
		Model:    req.Model,
		Variants: make([]apicontract.HypotheticalVariantFit, 0, len(variants)),
	}
	var tightFallback, hybridFallback string
	for _, v := range variants {
		shape, estimated := fit.ShapeFromDescriptor(d, v.FileBytes)
		resp.Estimated = resp.Estimated || estimated
		res := fit.AnalyzeShape(shape, p)
		vf := apicontract.HypotheticalVariantFit{
			Name:             v.Name,
			FitLevel:         apicontract.FitLevel(res.FitLevel),
			MaxSafeCtx:       res.MaxSafeCtx,
			ModelMb:          ptrOf(res.ModelMB),
			KvMbAtMaxSafeCtx: ptrOf(res.KVMBAtMaxSafeCtx),
			VramRequiredMb:   ptrOf(res.VRAMRequiredMB),
			Reason:           ptrOf(res.Reason),
		}
		if res.MaxFitCtx > 0 {
			vf.MaxFitCtx = ptrOf(res.MaxFitCtx)
		}
		// Placement verdict: distinguishes "hybrid-loadable" from "won't
		// fit" so a 90 GB quant on a 48 GB card ranks as loadable-with-
		// caveats. Descriptor shapes carry no tensor table, so MoE expert
		// placement is approximated as a dense spill (llamacpp only).
		if backend == apicontract.HypotheticalFitRequestBackendLlamacpp {
			plan := placement.Compute(placement.Inputs{
				Shape:           shape,
				ConfiguredCtx:   p.ConfiguredCtx,
				KCacheBits:      p.KCacheBits,
				VCacheBits:      p.VCacheBits,
				VRAMBudgetMB:    p.VRAMTotalMB,
				HostAvailableMB: hostAvailMB,
				HostTotalMB:     hostTotalMB,
				Policy:          s.cfg.Placement,
			})
			vf.Placement = ptrOf(apicontract.HypotheticalVariantFitPlacement(plan.Mode))
			if plan.Estimate.HostMB > 0 {
				vf.EstHostMb = ptrOf(plan.Estimate.HostMB)
			}
			if plan.Mode == placement.ModeHybrid && hybridFallback == "" {
				hybridFallback = v.Name
			}
		}
		resp.Variants = append(resp.Variants, vf)
		if resp.Recommended == nil {
			switch res.FitLevel {
			case "perfect", "good":
				resp.Recommended = ptrOf(v.Name)
			case "tight":
				if tightFallback == "" {
					tightFallback = v.Name
				}
			}
		}
	}
	if resp.Recommended == nil && tightFallback != "" {
		resp.Recommended = ptrOf(tightFallback)
	}
	// Nothing fits fully: the largest hybrid-loadable variant is still a
	// better recommendation than nothing (walk order is largest-first).
	if resp.Recommended == nil && hybridFallback != "" {
		resp.Recommended = ptrOf(hybridFallback)
	}
	if total, free := s.vramMB(); total > 0 {
		resp.VramTotalMb = ptrOf(total)
		resp.VramFreeMb = ptrOf(free)
	}
	writeJSON(w, resp)
}

// z64 dereferences an optional int64, treating absent as 0 (= estimate/derive).
func z64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
