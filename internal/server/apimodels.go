package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/router"
	"github.com/androidand/llama-skein/pkg/apicontract"
	"github.com/androidand/llama-skein/pkg/gguf"
)

// defaultRecommendationCtx is the context length assumed for offload/VRAM
// budgeting when a model's command does not pin --ctx-size.
const defaultRecommendationCtx int64 = 32768

// handleAPIListModels implements GET /api/models.
// Returns all configured models with runtime state, file metadata, and inferred
// details. Filter to loaded models only with ?state=running.
func (s *Server) handleAPIListModels(w http.ResponseWriter, r *http.Request) {
	onlyRunning := r.URL.Query().Get("state") == "running"
	ids := make([]string, 0, len(s.cfg.Models))
	for id := range s.cfg.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	entries := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		mc := s.cfg.Models[id]
		state, loaded := s.modelState(id)
		if onlyRunning && !loaded {
			continue
		}
		entry := map[string]any{
			"id":       id,
			"object":   "model",
			"state":    state,
			"loaded":   loaded,
			"unlisted": mc.Unlisted,
		}
		if name := strings.TrimSpace(mc.Name); name != "" {
			entry["name"] = name
		}
		if desc := strings.TrimSpace(mc.Description); desc != "" {
			entry["description"] = desc
		}
		if len(mc.Aliases) > 0 {
			entry["aliases"] = mc.Aliases
		}
		addFileMeta(entry, mc)
		filename := ""
		if p := parseModelPath(mc.Cmd); p != "" {
			filename = p[strings.LastIndexAny(p, "/\\")+1:]
		}
		entry["details"] = inferModelDetails(id, filename)
		entries = append(entries, entry)
	}

	writeJSON(w, map[string]any{"models": entries})
}

// handleAPIGetModel implements GET /api/models/{model}.
// Returns config, runtime state, file metadata, GGUF metadata, and inferred details.
func (s *Server) handleAPIGetModel(w http.ResponseWriter, r *http.Request) {
	requested := strings.TrimPrefix(r.PathValue("model"), "/")
	realName, found := s.cfg.RealModelName(requested)
	if !found {
		router.SendResponse(w, r, http.StatusNotFound, "model not found")
		return
	}
	mc := s.cfg.Models[realName]
	state, loaded := s.modelState(realName)

	record := map[string]any{
		"id":     realName,
		"object": "model",
		"state":  state,
		"loaded": loaded,
	}
	if name := strings.TrimSpace(mc.Name); name != "" {
		record["name"] = name
	}
	if desc := strings.TrimSpace(mc.Description); desc != "" {
		record["description"] = desc
	}
	addFileMeta(record, mc)
	filename := ""
	if p := parseModelPath(mc.Cmd); p != "" {
		filename = p[strings.LastIndexAny(p, "/\\")+1:]
	}
	record["details"] = inferModelDetails(realName, filename)
	addModelRuntimeHints(record, mc)
	addGGUFMetadata(record, mc)
	if len(mc.Metadata) > 0 {
		if metaMap, ok := record["meta"].(map[string]any); ok {
			metaMap["llamaswap"] = mc.Metadata
		} else {
			record["meta"] = map[string]any{"llamaswap": mc.Metadata}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(record)
}

// handleAPILoadModel implements POST /api/models/load/{model}.
// Warms a model by routing a minimal inference request through the local router.
func (s *Server) handleAPILoadModel(w http.ResponseWriter, r *http.Request) {
	requested := strings.TrimPrefix(r.PathValue("model"), "/")
	realName, found := s.cfg.RealModelName(requested)
	if !found {
		router.SendResponse(w, r, http.StatusNotFound, "model not found")
		return
	}
	if !s.local.Handles(realName) {
		router.SendResponse(w, r, http.StatusNotFound, "no local server found for model")
		return
	}

	body := fmt.Sprintf(
		`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_tokens":1,"stream":false}`,
		realName,
	)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError, "failed to build load request: "+err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(router.SetContext(req.Context(), router.ReqContextData{
		Model:   realName,
		ModelID: realName,
	}))

	dw := &discardResponseWriter{status: http.StatusOK}
	s.local.ServeHTTP(dw, req)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// handleAPIDeleteModel implements DELETE /api/models/{model}.
// Unloads the model (if running) then deletes its weight file from disk.
func (s *Server) handleAPIDeleteModel(w http.ResponseWriter, r *http.Request) {
	requested := strings.TrimPrefix(r.PathValue("model"), "/")
	realName, found := s.cfg.RealModelName(requested)
	if !found {
		router.SendResponse(w, r, http.StatusNotFound, "model not found")
		return
	}

	mc := s.cfg.Models[realName]
	filePath := parseModelPath(mc.Cmd)
	if filePath == "" {
		router.SendResponse(w, r, http.StatusUnprocessableEntity,
			fmt.Sprintf("cannot determine model file path for %q (no -m/--model in cmd)", realName))
		return
	}

	if s.local.Handles(realName) {
		s.local.Unload(0, realName)
	}

	if err := removeFile(filePath); err != nil {
		if isNotExist(err) {
			router.SendResponse(w, r, http.StatusNotFound,
				fmt.Sprintf("model file not found on disk: %s", filePath))
			return
		}
		router.SendResponse(w, r, http.StatusInternalServerError,
			fmt.Sprintf("failed to delete %s: %v", filePath, err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"deleted": filePath,
		"model":   realName,
	})
}

// handleAPIContextRecommendation implements GET /api/models/context/{model}.
// Returns recommended context window based on GGUF metadata and available memory.
func (s *Server) handleAPIContextRecommendation(w http.ResponseWriter, r *http.Request) {
	requested := strings.TrimPrefix(r.PathValue("model"), "/")
	realName, found := s.cfg.RealModelName(requested)
	if !found {
		router.SendResponse(w, r, http.StatusNotFound, "model not found")
		return
	}

	mc := s.cfg.Models[realName]
	ggufPath := parseModelPath(mc.Cmd)
	if ggufPath == "" {
		writeJSON(w, map[string]any{"recommended": 8192, "modelFileGB": 0, "min": 8192, "max": 0})
		return
	}

	g, err := gguf.ParseFile(ggufPath)
	if err != nil {
		writeJSON(w, map[string]any{"recommended": 8192, "modelFileGB": 0, "min": 8192, "max": 0})
		return
	}

	freeBytes, _ := s.freeVRAMBytes()

	if freeBytes <= 0 {
		writeJSON(w, map[string]any{"recommended": 8192, "modelFileGB": 0, "min": 8192, "max": 0})
		return
	}

	minCtx := g.MinCtxSize()
	maxCtx := g.MaxCtxSize(freeBytes)
	if maxCtx <= 0 {
		maxCtx = minCtx
	}
	if maxCtx > 262144 {
		maxCtx = 262144
	}
	maxCtx = (maxCtx / 1024) * 1024
	if maxCtx < 8192 {
		maxCtx = 8192
	}

	modelFileGB := float64(g.WeightBytes()) / (1 << 30)
	writeJSON(w, map[string]any{
		"recommended": maxCtx,
		"modelFileGB": modelFileGB,
		"min":         minCtx,
		"max":         maxCtx,
	})
}

// freeVRAMBytes returns the free VRAM budget in bytes and megabytes, from the
// latest performance snapshot. Semantics (multi-GPU sum, unified wired-limit
// cap, available-memory fallback) live in hostVRAM via vramMB.
func (s *Server) freeVRAMBytes() (bytes int64, mb int) {
	_, mb = s.vramMB()
	return int64(mb) << 20, mb
}

// handleAPIOffloadRecommendation implements GET /api/models/offload/{model}.
// It recommends a --n-cpu-moe value from GGUF expert tensor sizes and current
// free VRAM. MoE-scoped: non-MoE models and non-llamacpp backends return
// applicable=false with a reason.
func (s *Server) handleAPIOffloadRecommendation(w http.ResponseWriter, r *http.Request) {
	requested := strings.TrimPrefix(r.PathValue("model"), "/")
	realName, found := s.cfg.RealModelName(requested)
	if !found {
		router.SendResponse(w, r, http.StatusNotFound, "model not found")
		return
	}
	mc := s.cfg.Models[realName]

	backend := mc.Backend
	if backend == "" {
		backend = config.BackendLlamaCpp
	}
	resp := apicontract.OffloadRecommendation{
		Backend: apicontract.OffloadRecommendationBackend(backend),
	}

	if backend != config.BackendLlamaCpp {
		resp.Reason = stringPtr("offload recommendation is only computed for the llamacpp backend")
		writeJSON(w, resp)
		return
	}

	ggufPath := parseModelPath(mc.Cmd)
	if ggufPath == "" {
		resp.Reason = stringPtr("no model file (-m/--model) found in the model command")
		writeJSON(w, resp)
		return
	}
	g, err := gguf.ParseFile(ggufPath)
	if err != nil {
		resp.Reason = stringPtr(fmt.Sprintf("could not read GGUF metadata: %v", err))
		writeJSON(w, resp)
		return
	}

	freeBytes, freeMB := s.freeVRAMBytes()
	if freeMB > 0 {
		resp.VramFreeMb = &freeMB
	}

	// Budget the KV cache against the configured context, else the trained one.
	ctxLen := defaultRecommendationCtx
	args, _ := mc.SanitizedCommand()
	if v, ok := commandFlagInt(args, "--ctx-size", "-c"); ok {
		ctxLen = int64(v)
	} else if g.ContextLength > 0 && g.ContextLength < ctxLen {
		ctxLen = g.ContextLength
	}
	ctxInt := int(ctxLen)
	resp.CtxSize = &ctxInt

	plan := g.RecommendCpuMoe(freeBytes, ctxLen)
	resp.Applicable = plan.Applicable
	resp.Reason = stringPtr(plan.Reason)
	if plan.ExpertBytesTotal > 0 {
		eb := int(plan.ExpertBytesTotal)
		resp.ExpertBytesTotal = &eb
	}
	if plan.Applicable {
		n := plan.NCpuMoe
		resp.NCpuMoe = &n
		fits := plan.FitsFullyOnGPU
		resp.FitsFullyOnGpu = &fits
	}
	writeJSON(w, resp)
}

// writeJSON encodes v as JSON with the correct content-type header.
func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, http.StatusOK, v)
}

// writeJSONStatus encodes v as JSON with the given status code.
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
