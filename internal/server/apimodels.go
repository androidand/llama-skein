package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/fit"
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

	// task 5.1: built once and reused per model — s.operationStore.List()
	// is already a full scan of every operation record regardless of how
	// many models ask, so one scan here is strictly better than one per
	// model.
	opIdx := s.buildModelOperationIndex()
	// task 5.2: same reasoning as opIdx — s.local.ModelErrors() is already
	// a full scan of every process's recorded failure; the Model schema
	// has documented a last_error field since before this change, but
	// neither this handler nor handleAPIGetModel ever actually populated
	// it (only the unrelated health endpoint, api.go, did) until now.
	modelErrs := s.local.ModelErrors()

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
		if le, ok := modelErrs[id]; ok {
			entry["last_error"] = le
		}
		addFileMeta(entry, mc)
		addProvenanceAndOperationFields(entry, id, mc, opIdx)
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
	if le, ok := s.local.ModelErrors()[realName]; ok {
		record["last_error"] = le
	}
	if name := strings.TrimSpace(mc.Name); name != "" {
		record["name"] = name
	}
	if desc := strings.TrimSpace(mc.Description); desc != "" {
		record["description"] = desc
	}
	addFileMeta(record, mc)
	addProvenanceAndOperationFields(record, realName, mc, s.buildModelOperationIndex())
	filename := ""
	if p := parseModelPath(mc.Cmd); p != "" {
		filename = p[strings.LastIndexAny(p, "/\\")+1:]
	}
	record["details"] = inferModelDetails(realName, filename)
	addModelRuntimeHints(record, mc)
	s.addPlacementHint(record, realName)
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

	// task 5.2: report the actual outcome instead of always claiming
	// success — dw.status was already captured before this task but never
	// checked, so this endpoint silently reported "OK" even when the warm
	// request failed outright. Both signals are checked because they can
	// disagree: the warm request itself can fail (e.g. time out) while the
	// process is still "starting", and conversely the request can succeed
	// for a process that later reports "failed" from something unrelated
	// to this specific warm call. The success response body is
	// deliberately left exactly as it was (plain "OK" text, 200) — no
	// caller could have been depending on a specific failure response
	// before, since failure was never reported at all, but changing the
	// success shape would be a needless breaking change to anything
	// already parsing this endpoint.
	state, loaded := s.modelState(realName)
	if dw.status >= http.StatusBadRequest || state == "failed" {
		body := map[string]any{
			"model":               realName,
			"state":               state,
			"loaded":              loaded,
			"load_request_status": dw.status,
		}
		if le, ok := s.local.ModelErrors()[realName]; ok {
			body["last_error"] = le
		}
		writeJSONStatus(w, http.StatusBadGateway, body)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// handleAPIDeleteModel implements DELETE /api/models/{model}.
//
// task 5.3 (design.md decision 6: "Remove unloads first, validates
// ownership/path, removes the complete installed artifact set, and removes
// config in one explicit operation"): unloads the model, validates every
// candidate file is contained within the configured models directory,
// removes the complete artifact set (not just the primary weights file —
// see resolveArtifactSetForRemoval), then removes the config entry.
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

	// Resolve and validate the complete artifact set before touching
	// anything on disk or in config — a failure here (unknown models
	// directory, or a path that escapes it) aborts the whole request; never
	// partially delete under an unvalidated set.
	paths, err := s.resolveArtifactSetForRemoval(realName, filePath, s.buildModelOperationIndex())
	if err != nil {
		router.SendResponse(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Unload before touching any file — design.md decision 6's documented
	// order ("Remove unloads first").
	if s.local.Handles(realName) {
		s.local.Unload(0, realName)
	}

	deleted := make([]string, 0, len(paths))
	var missing []string
	for _, p := range paths {
		if err := removeFile(p); err != nil {
			if isNotExist(err) {
				missing = append(missing, p)
				continue
			}
			router.SendResponse(w, r, http.StatusInternalServerError,
				fmt.Sprintf("failed to delete %s: %v (already removed in this request: %v)", p, err, deleted))
			return
		}
		deleted = append(deleted, p)
	}
	// Every candidate path already missing (not one partial hit) is the
	// only case treated as "nothing to delete" — a shard set where most
	// files are gone but one remains still counts as real removal work.
	if len(deleted) == 0 && len(missing) == len(paths) {
		router.SendResponse(w, r, http.StatusNotFound,
			fmt.Sprintf("no artifact files found on disk for %q (expected: %v)", realName, paths))
		return
	}

	// task 5.3: "removes config in one explicit operation" — reuses the
	// same config-removal path DELETE /api/models/config/{id} already uses
	// (apiconfig.go's removeModelFromConfig) rather than duplicating it.
	// Best-effort when configFile isn't set: the files are already gone by
	// this point, so that real progress is reported rather than discarded
	// behind an error.
	configRemoved := false
	if s.configFile != "" {
		changed, err := s.removeModelFromConfig(realName)
		if err != nil {
			router.SendResponse(w, r, http.StatusInternalServerError,
				fmt.Sprintf("deleted artifact files but failed to remove config entry: %v", err))
			return
		}
		configRemoved = true // true either way: the model is confirmed absent from config now, whether this call was what removed it or it was already gone.
		// task 5.4: only reload if something on disk actually changed —
		// an already-absent config entry needs no reload to "confirm" its
		// absence again.
		if changed {
			s.runtimeStateOrDefault().SetPending("api:delete-model", "deleted model "+realName)
			s.triggerReload()
		}
	}

	writeJSONStatus(w, http.StatusOK, map[string]any{
		"model": realName,
		// "deleted" is kept for backward compatibility with callers of the
		// pre-5.3 single-file response — the primary weights path, same
		// meaning it always had.
		"deleted":        filePath,
		"deleted_files":  deleted,
		"missing_files":  missing,
		"config_removed": configRemoved,
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
		Backend: apicontract.Backend(backend),
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

	// Cache-type-aware KV estimate (single source of truth with /api/fit):
	// the legacy FP16-only KVCacheBytes over-budgets quantized-KV commands.
	kBits, vBits := 16.0, 16.0
	if kc, ok := commandFlagString(args, "--cache-type-k", "-ctk"); ok {
		kBits = fit.BitsPerElement(kc)
	}
	if vc, ok := commandFlagString(args, "--cache-type-v", "-ctv"); ok {
		vBits = fit.BitsPerElement(vc)
	}
	plan := g.RecommendCpuMoe(freeBytes, ctxLen, fit.KVBytesPerToken(g, kBits, vBits))
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
