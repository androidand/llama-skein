package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/androidand/llama-skein/internal/process"
)

// This file restores the fork's legacy API surface that was dropped in the
// gin → net/http migration. skein consumes these paths directly
// (llamaswap_client.go, adapters/ollama, providers/connect.go probing), and
// Ollama-mode frontends (Open WebUI, Chatbox, Msty) speak /api/tags + /api/show.
//
// /api/events, /api/resources, and /api/storage are pure aliases: the new
// handlers emit the same wire shapes (messageEnvelope SSE, the /api/hardware
// superset, and diskStorageStats respectively), only the paths moved.

// --- Ollama-compatible types (ported verbatim from proxy/proxymanager_ollama.go) ---

type ollamaModelDetails struct {
	ParentModel       string   `json:"parent_model"`
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
}

type ollamaModelEntry struct {
	Name       string             `json:"name"`
	Model      string             `json:"model"`
	ModifiedAt time.Time          `json:"modified_at"`
	Size       int64              `json:"size"`
	Digest     string             `json:"digest"`
	Details    ollamaModelDetails `json:"details"`
}

// ollamaError matches the {"error": "..."} shape Ollama clients parse.
func ollamaError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// handleAPIPS implements GET /api/ps — currently loaded models in an
// Ollama-compatible envelope: {"models": [{"name", "model", "state"}]}.
func (s *Server) handleAPIPS(w http.ResponseWriter, r *http.Request) {
	type psEntry struct {
		Name  string `json:"name"`
		Model string `json:"model"`
		State string `json:"state"`
	}
	running := make([]psEntry, 0)
	for id, st := range s.local.RunningModels() {
		if st != process.StateReady {
			continue
		}
		mc, _, ok := s.cfg.FindConfig(id)
		name := id
		if ok && strings.TrimSpace(mc.Name) != "" {
			name = strings.TrimSpace(mc.Name)
		}
		running = append(running, psEntry{Name: name, Model: id, State: string(st)})
	}
	sort.Slice(running, func(i, j int) bool { return running[i].Model < running[j].Model })
	writeJSON(w, map[string]any{"models": running})
}

// handleOllamaTags implements GET /api/tags — Ollama-compatible model list.
func (s *Server) handleOllamaTags(w http.ResponseWriter, r *http.Request) {
	modelIDs := make([]string, 0, len(s.cfg.Models))
	for id := range s.cfg.Models {
		modelIDs = append(modelIDs, id)
	}
	sort.Strings(modelIDs)

	entries := make([]ollamaModelEntry, 0, len(modelIDs))
	for _, id := range modelIDs {
		mc := s.cfg.Models[id]
		if mc.Unlisted {
			continue
		}
		filename := ""
		if p := parseModelPath(mc.Cmd); p != "" {
			filename = filepath.Base(p)
		}
		entry := ollamaModelEntry{
			Name:    id,
			Model:   id,
			Digest:  "",
			Details: toOllamaDetails(inferModelDetails(id, filename)),
		}
		if p := parseModelPath(mc.Cmd); p != "" {
			if fi, err := os.Stat(p); err == nil {
				entry.Size = fi.Size()
				entry.ModifiedAt = fi.ModTime()
			}
		}
		if entry.ModifiedAt.IsZero() {
			entry.ModifiedAt = time.Now()
		}
		entries = append(entries, entry)
	}
	writeJSON(w, map[string]any{"models": entries})
}

// handleOllamaShow implements POST /api/show — Ollama model details.
func (s *Server) handleOllamaShow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model   string `json:"model"`
		Name    string `json:"name"`
		Verbose bool   `json:"verbose"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	name := req.Model
	if name == "" {
		name = req.Name
	}
	if name == "" {
		ollamaError(w, http.StatusBadRequest, "model or name field required")
		return
	}
	realName, found := s.cfg.RealModelName(name)
	if !found {
		ollamaError(w, http.StatusNotFound, "model not found")
		return
	}
	mc := s.cfg.Models[realName]
	filename := ""
	if p := parseModelPath(mc.Cmd); p != "" {
		filename = filepath.Base(p)
	}

	resp := map[string]any{
		"model":      realName,
		"details":    toOllamaDetails(inferModelDetails(realName, filename)),
		"model_info": map[string]any{},
		"template":   "",
	}
	if mc.Name != "" {
		resp["name"] = mc.Name
	}
	if mc.Description != "" {
		resp["description"] = mc.Description
	}
	if p := parseModelPath(mc.Cmd); p != "" {
		if fi, err := os.Stat(p); err == nil {
			resp["modified_at"] = fi.ModTime()
			resp["size"] = fi.Size()
		}
	}
	writeJSON(w, resp)
}

// handleOllamaDelete implements DELETE /api/delete — Ollama-compatible model
// delete. Body: {"name": "model-id"} or {"model": "model-id"}. Unloads the
// process then removes the weight file from disk.
func (s *Server) handleOllamaDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ollamaError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := req.Name
	if name == "" {
		name = req.Model
	}
	if name == "" {
		ollamaError(w, http.StatusBadRequest, "name or model field required")
		return
	}

	realName, found := s.cfg.RealModelName(name)
	if !found {
		ollamaError(w, http.StatusNotFound, "model not found")
		return
	}
	filePath := parseModelPath(s.cfg.Models[realName].Cmd)
	if filePath == "" {
		ollamaError(w, http.StatusUnprocessableEntity,
			fmt.Sprintf("cannot determine model file path for %q", realName))
		return
	}

	s.local.Unload(0, realName)

	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		ollamaError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to delete %s: %v", filePath, err))
		return
	}
	// Ollama returns 200 with empty body on success.
	w.WriteHeader(http.StatusOK)
}

// toOllamaDetails converts the shared inferred metadata to the exact JSON
// shape Ollama clients parse (quantization_level, families list, etc.).
func toOllamaDetails(d ModelDetails) ollamaModelDetails {
	out := ollamaModelDetails{
		Format:            d.Format,
		Family:            d.Family,
		Families:          []string{},
		ParameterSize:     d.ParameterSize,
		QuantizationLevel: d.QuantizationLevel,
	}
	if d.Family != "" && d.Family != "unknown" {
		out.Families = []string{d.Family}
	}
	return out
}
