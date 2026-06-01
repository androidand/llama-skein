package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/androidand/llama-skein/internal/router"
)

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

// handleAPIOllamaTags implements GET /api/tags — Ollama-compatible model list.
func (s *Server) handleAPIOllamaTags(w http.ResponseWriter, r *http.Request) {
	ids := make([]string, 0, len(s.cfg.Models))
	for id := range s.cfg.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	entries := make([]ollamaModelEntry, 0, len(ids))
	for _, id := range ids {
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
			Details: inferModelDetails(id, filename),
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"models": entries})
}

// handleAPIOllamaShow implements POST /api/show — Ollama model details.
func (s *Server) handleAPIOllamaShow(w http.ResponseWriter, r *http.Request) {
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
		router.SendResponse(w, r, http.StatusBadRequest, "model or name field required")
		return
	}
	realName, found := s.cfg.RealModelName(name)
	if !found {
		router.SendResponse(w, r, http.StatusNotFound, "model not found")
		return
	}
	mc := s.cfg.Models[realName]
	filename := ""
	if p := parseModelPath(mc.Cmd); p != "" {
		filename = filepath.Base(p)
	}
	details := inferModelDetails(realName, filename)

	resp := map[string]any{
		"model":      realName,
		"details":    details,
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleAPIOllamaDelete implements DELETE /api/delete — Ollama-compatible delete.
// Body: {"name": "model-id"} or {"model": "model-id"}.
func (s *Server) handleAPIOllamaDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		router.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	name := req.Name
	if name == "" {
		name = req.Model
	}
	if name == "" {
		router.SendResponse(w, r, http.StatusBadRequest, "name or model field required")
		return
	}

	realName, found := s.cfg.RealModelName(name)
	if !found {
		router.SendResponse(w, r, http.StatusNotFound, "model not found")
		return
	}
	mc := s.cfg.Models[realName]
	filePath := parseModelPath(mc.Cmd)
	if filePath == "" {
		router.SendResponse(w, r, http.StatusUnprocessableEntity,
			"cannot determine model file path for "+realName)
		return
	}

	if s.local.Handles(realName) {
		s.local.Unload(0, realName)
	}

	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		router.SendResponse(w, r, http.StatusInternalServerError,
			"failed to delete "+filePath+": "+err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

// inferModelDetails derives Ollama-style metadata from a model ID and filename.
func inferModelDetails(id, filename string) ollamaModelDetails {
	lower := strings.ToLower(id + " " + filename)
	d := ollamaModelDetails{
		Format:   "gguf",
		Family:   "unknown",
		Families: []string{},
	}

	for _, q := range []string{
		"iq4_nl", "iq3_m", "iq2_m",
		"q4_k_m", "q4_k_s", "q5_k_m", "q5_k_s", "q6_k", "q8_0", "q4_0", "q2_k",
	} {
		if strings.Contains(lower, q) {
			d.QuantizationLevel = strings.ToUpper(q)
			break
		}
	}

	for _, size := range []string{
		"110b", "90b", "72b", "70b", "35b", "32b", "30b", "27b", "24b", "14b", "13b",
		"9b", "8b", "7b", "3b", "1.5b", "1b", "0.5b",
	} {
		if strings.Contains(lower, size) {
			d.ParameterSize = strings.ToUpper(size)
			break
		}
	}

	families := []struct{ key, name string }{
		{"codellama", "codellama"}, {"deepseek", "deepseek"}, {"starcoder", "starcoder"},
		{"mixtral", "mixtral"}, {"mistral", "mistral"}, {"llama", "llama"},
		{"qwen", "qwen"}, {"phi", "phi"}, {"gemma", "gemma"}, {"falcon", "falcon"},
		{"solar", "solar"}, {"yi", "yi"}, {"smollm", "llama"},
	}
	for _, f := range families {
		if strings.Contains(lower, f.key) {
			d.Family = f.name
			d.Families = []string{f.name}
			break
		}
	}

	return d
}
