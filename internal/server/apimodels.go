package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/androidand/llama-skein/internal/router"
	"github.com/androidand/llama-skein/pkg/gguf"
)

// handleAPILoadModel warms a model by routing a minimal inference request
// through the local router, causing the process to start and load weights.
// POST /api/models/load/{model...}
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

// handleAPIGetModel returns the config and current runtime state for a single
// model. GET /api/models/{model...}
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

// handleAPIPS returns only the models that are currently loaded (state==ready).
// GET /api/ps
func (s *Server) handleAPIPS(w http.ResponseWriter, r *http.Request) {
	running := make([]map[string]any, 0)
	for id, mc := range s.cfg.Models {
		state, loaded := s.modelState(id)
		if !loaded {
			continue
		}
		name := strings.TrimSpace(mc.Name)
		if name == "" {
			name = id
		}
		running = append(running, map[string]any{
			"name":  name,
			"model": id,
			"state": state,
		})
	}
	sort.Slice(running, func(i, j int) bool {
		return running[i]["model"].(string) < running[j]["model"].(string)
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"models": running})
}

// handleAPIDeleteModel unloads a model (if running) then deletes its weight
// file from disk. Config entry is preserved.
// DELETE /api/models/{model...}
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

// handleAPIContextRecommendation returns the recommended context window for
// a model based on its GGUF metadata and available memory.
// POST /api/models/context-recommendation/{model...}
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

	var freeBytes int64
	if s.perf != nil {
		sysStats, gpuStats := s.perf.Current()
		if len(sysStats) > 0 {
			sys := sysStats[len(sysStats)-1]
			if len(gpuStats) > 0 {
				gpu := gpuStats[0]
				freeBytes = int64(gpu.MemTotalMB-gpu.MemUsedMB) << 20
			} else {
				freeBytes = int64(sys.MemFreeMB) << 20
			}
		}
	}

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

// handleAPIStorage returns disk space for the models directory.
// GET /api/storage
func (s *Server) handleAPIStorage(w http.ResponseWriter, r *http.Request) {
	dir := s.modelsDir()
	if dir == "" {
		router.SendResponse(w, r, http.StatusUnprocessableEntity,
			"models directory unknown; set modelsDir in config or use --models-dir flag")
		return
	}
	storageStats(w, r, dir)
}

// writeJSON is a tiny helper to encode JSON and set the content-type header.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
