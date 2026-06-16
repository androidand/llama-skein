package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/offload"
	"github.com/androidand/llama-skein/internal/router"
	"github.com/androidand/llama-skein/pkg/apicontract"
	"gopkg.in/yaml.v3"
)

// The config API request bodies are decoded directly into the generated
// apicontract types (the OpenAPI spec is the source of truth) rather than
// hand-written mirrors. The helpers below adapt those generated types to the
// internal config model.

// offloadSpecFromAddRequest collects offload knobs from an add-model request.
func offloadSpecFromAddRequest(req apicontract.ConfigModelRequest) offload.Spec {
	return offload.Spec{
		NCpuMoe:        req.NCpuMoe,
		CpuMoe:         req.CpuMoe,
		CpuOffloadGB:   req.CpuOffloadGb,
		OverrideTensor: req.OverrideTensor,
	}
}

// offloadSpecFromPatchRequest collects offload knobs from a patch request. The
// dash-named alias wins when both forms are present, matching the
// ctx_size/ctx-size rule.
func offloadSpecFromPatchRequest(req apicontract.ConfigModelPatchRequest) offload.Spec {
	s := offload.Spec{
		NCpuMoe:        req.NCpuMoe,
		CpuMoe:         req.CpuMoe,
		CpuOffloadGB:   req.CpuOffloadGb,
		OverrideTensor: req.OverrideTensor,
	}
	if req.NCpuMoeDash != nil {
		s.NCpuMoe = req.NCpuMoeDash
	}
	if req.CpuOffloadGBDash != nil {
		s.CpuOffloadGB = req.CpuOffloadGBDash
	}
	if req.OverrideTensorDash != nil {
		s.OverrideTensor = req.OverrideTensorDash
	}
	return s
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// handleAPIConfigInfo implements GET /api/config/info.
func (s *Server) handleAPIConfigInfo(w http.ResponseWriter, r *http.Request) {
	type modelInfo struct {
		ID         string `json:"id"`
		FilePath   string `json:"file_path,omitempty"`
		FileExists bool   `json:"file_exists"`
	}

	models := make([]modelInfo, 0, len(s.cfg.Models))
	for id, mc := range s.cfg.Models {
		mi := modelInfo{ID: id}
		if p := parseModelPath(mc.Cmd); p != "" {
			mi.FilePath = p
			_, err := os.Stat(p)
			mi.FileExists = err == nil
		}
		models = append(models, mi)
	}

	info := map[string]any{
		"config_file": s.configFile,
		"models_dir":  s.modelsDir(),
		"model_count": len(s.cfg.Models),
		"models":      models,
	}
	if s.cfg.DefaultModel != "" {
		info["default_model"] = s.cfg.DefaultModel
	}
	writeJSON(w, info)
}

// handleAPIConfigGetDefaultModel implements GET /api/config/default-model.
func (s *Server) handleAPIConfigGetDefaultModel(w http.ResponseWriter, r *http.Request) {
	var model any
	if s.cfg.DefaultModel != "" {
		model = s.cfg.DefaultModel
	}
	writeJSON(w, map[string]any{"model": model})
}

// handleAPIConfigSetDefaultModel implements PUT /api/config/default-model.
func (s *Server) handleAPIConfigSetDefaultModel(w http.ResponseWriter, r *http.Request) {
	var req apicontract.ConfigDefaultModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		router.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if req.Model == "" {
		router.SendResponse(w, r, http.StatusBadRequest, "model is required")
		return
	}
	if _, found := s.cfg.RealModelName(req.Model); !found {
		router.SendResponse(w, r, http.StatusNotFound, "model not found in config")
		return
	}
	if s.configFile == "" {
		router.SendResponse(w, r, http.StatusUnprocessableEntity, "config file path not set")
		return
	}
	if err := s.writeDefaultModelToConfig(req.Model); err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError,
			fmt.Sprintf("write config: %v", err))
		return
	}
	s.triggerReload()
	writeJSONStatus(w, http.StatusAccepted, map[string]any{"id": req.Model, "status": "updated"})
}

// handleAPIConfigClearDefaultModel implements DELETE /api/config/default-model.
func (s *Server) handleAPIConfigClearDefaultModel(w http.ResponseWriter, r *http.Request) {
	if s.configFile == "" {
		router.SendResponse(w, r, http.StatusUnprocessableEntity, "config file path not set")
		return
	}
	if err := s.writeDefaultModelToConfig(""); err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError,
			fmt.Sprintf("write config: %v", err))
		return
	}
	s.triggerReload()
	writeJSONStatus(w, http.StatusAccepted, map[string]any{"id": s.cfg.DefaultModel, "status": "removed"})
}

// handleAPIConfigAddModel implements POST /api/config/models.
func (s *Server) handleAPIConfigAddModel(w http.ResponseWriter, r *http.Request) {
	var req apicontract.ConfigModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		router.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if req.Id == "" || req.Cmd == "" {
		router.SendResponse(w, r, http.StatusBadRequest, "id and cmd are required")
		return
	}
	if !isValidModelID(req.Id) {
		router.SendResponse(w, r, http.StatusBadRequest,
			"model ID contains invalid characters; use A-Za-z0-9 . _ : / -")
		return
	}
	if s.configFile == "" {
		router.SendResponse(w, r, http.StatusUnprocessableEntity,
			"config file path not set; restart llama-skein with --config flag")
		return
	}

	backend := ""
	if req.Backend != nil {
		backend = string(*req.Backend)
	}
	mc := config.ModelConfig{
		Cmd:         req.Cmd,
		Backend:     backend,
		Name:        derefString(req.Name),
		Description: derefString(req.Description),
		UnloadAfter: config.MODEL_CONFIG_DEFAULT_TTL,
	}
	if req.Aliases != nil {
		mc.Aliases = *req.Aliases
	}
	if req.Ttl != nil {
		mc.UnloadAfter = *req.Ttl
	}

	var warnings []string
	if spec := offloadSpecFromAddRequest(req); !spec.Empty() {
		ops, warn := offload.For(mc.Backend).Ops(spec)
		warnings = warn
		if len(ops) > 0 {
			patched, err := applyFlagOps(mc.Cmd, ops)
			if err != nil {
				router.SendResponse(w, r, http.StatusBadRequest,
					fmt.Sprintf("apply offload flags: %v", err))
				return
			}
			mc.Cmd = patched
		}
	}

	if err := s.writeModelToConfig(req.Id, &mc); err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError,
			fmt.Sprintf("write config: %v", err))
		return
	}
	s.triggerReload()
	resp := map[string]any{"id": req.Id, "status": "added"}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	writeJSONStatus(w, http.StatusAccepted, resp)
}

// handleAPIConfigGetModel implements GET /api/config/models/{id}.
func (s *Server) handleAPIConfigGetModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	realID, found := s.cfg.RealModelName(id)
	if !found {
		router.SendResponse(w, r, http.StatusNotFound, "model not found in config")
		return
	}
	mc := s.cfg.Models[realID]

	flags := map[string]string{}
	if parts, err := config.SanitizeCommand(mc.Cmd); err == nil {
		for i := 1; i < len(parts); i++ {
			if !strings.HasPrefix(parts[i], "--") {
				continue
			}
			if i+1 < len(parts) && !strings.HasPrefix(parts[i+1], "--") {
				flags[parts[i]] = parts[i+1]
				i++
			} else {
				flags[parts[i]] = "true"
			}
		}
	}

	writeJSON(w, map[string]any{
		"id":               realID,
		"cmd":              mc.Cmd,
		"name":             mc.Name,
		"description":      mc.Description,
		"aliases":          mc.Aliases,
		"ttl":              mc.UnloadAfter,
		"concurrencyLimit": mc.ConcurrencyLimit,
		"ctx_size":         flags["--ctx-size"],
		"n_gpu_layers":     flags["--n-gpu-layers"],
		"cache_type_k":     flags["--cache-type-k"],
		"cache_type_v":     flags["--cache-type-v"],
		"flags":            flags,
	})
}

// handleAPIConfigPatchModel implements PATCH /api/config/models/{id}.
func (s *Server) handleAPIConfigPatchModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	realID, found := s.cfg.RealModelName(id)
	if !found {
		router.SendResponse(w, r, http.StatusNotFound, "model not found in config")
		return
	}
	if s.configFile == "" {
		router.SendResponse(w, r, http.StatusUnprocessableEntity, "config file path not set")
		return
	}

	var req apicontract.ConfigModelPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		router.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	warnings, err := s.patchModelInConfig(realID, req)
	if err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError,
			fmt.Sprintf("write config: %v", err))
		return
	}
	s.triggerReload()
	resp := map[string]any{"id": realID, "status": "updated"}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	writeJSONStatus(w, http.StatusAccepted, resp)
}

// handleAPIConfigRemoveModel implements DELETE /api/config/models/{id}.
func (s *Server) handleAPIConfigRemoveModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	realID, found := s.cfg.RealModelName(id)
	if !found {
		router.SendResponse(w, r, http.StatusNotFound, "model not found in config")
		return
	}
	if s.configFile == "" {
		router.SendResponse(w, r, http.StatusUnprocessableEntity, "config file path not set")
		return
	}
	if err := s.removeModelFromConfig(realID); err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError,
			fmt.Sprintf("write config: %v", err))
		return
	}
	s.triggerReload()
	writeJSONStatus(w, http.StatusAccepted, map[string]any{"id": realID, "status": "removed"})
}

// handleAPIConfigPatchGroup implements PATCH /api/config/groups/{id}.
func (s *Server) handleAPIConfigPatchGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, found := s.cfg.Groups[id]; !found {
		router.SendResponse(w, r, http.StatusNotFound, "group not found in config")
		return
	}
	if s.configFile == "" {
		router.SendResponse(w, r, http.StatusUnprocessableEntity, "config file path not set")
		return
	}

	var req apicontract.ConfigGroupPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		router.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.patchGroupInConfig(id, req); err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError,
			fmt.Sprintf("write config: %v", err))
		return
	}
	s.triggerReload()
	writeJSONStatus(w, http.StatusAccepted, map[string]any{"id": id, "status": "updated"})
}

// handleAPIConfigReload implements POST /api/config/reload.
func (s *Server) handleAPIConfigReload(w http.ResponseWriter, r *http.Request) {
	if s.reloadFn == nil {
		router.SendResponse(w, r, http.StatusServiceUnavailable,
			"reload not available; restart llama-skein manually")
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]any{"status": "reloading"})
	go s.reloadFn()
}

// triggerReload calls reloadFn in a goroutine if set.
func (s *Server) triggerReload() {
	if s.reloadFn != nil {
		go s.reloadFn()
	}
}

// writeDefaultModelToConfig sets or removes (model == "") the top-level
// defaultModel key in the config YAML.
func (s *Server) writeDefaultModelToConfig(model string) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	root, err := readYAMLRoot(s.configFile)
	if err != nil {
		return err
	}
	if model == "" {
		yamlMapDelete(root, "defaultModel")
	} else {
		yamlMapSet(root, "defaultModel", yamlScalar(model))
	}
	return writeYAMLRoot(s.configFile, root, 0o644)
}

// writeModelToConfig reads the config YAML, sets models[id]=mc, and writes back.
func (s *Server) writeModelToConfig(id string, mc *config.ModelConfig) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	root, err := readYAMLRoot(s.configFile)
	if err != nil {
		return err
	}

	modelsNode := yamlMapGet(root, "models")
	if modelsNode == nil {
		modelsNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		yamlMapSet(root, "models", modelsNode)
	}

	entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	yamlMapSet(entry, "cmd", yamlScalar(mc.Cmd))
	if mc.Backend != "" {
		yamlMapSet(entry, "backend", yamlScalar(mc.Backend))
	}
	if mc.Proxy != "" {
		yamlMapSet(entry, "proxy", yamlScalar(mc.Proxy))
	}
	if mc.Name != "" {
		yamlMapSet(entry, "name", yamlScalar(mc.Name))
	}
	if mc.Description != "" {
		yamlMapSet(entry, "description", yamlScalar(mc.Description))
	}
	if len(mc.Aliases) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, a := range mc.Aliases {
			seq.Content = append(seq.Content, yamlScalar(a))
		}
		yamlMapSet(entry, "aliases", seq)
	}
	if mc.UnloadAfter != config.MODEL_CONFIG_DEFAULT_TTL {
		yamlMapSet(entry, "ttl", yamlInt(mc.UnloadAfter))
	}
	if mc.ConcurrencyLimit != 0 {
		yamlMapSet(entry, "concurrencyLimit", yamlInt(mc.ConcurrencyLimit))
	}
	yamlMapSet(modelsNode, id, entry)

	return writeYAMLRoot(s.configFile, root, 0o644)
}

// patchModelInConfig applies a partial model update, preserving other fields.
// It returns any warnings produced while translating offload settings.
func (s *Server) patchModelInConfig(id string, req apicontract.ConfigModelPatchRequest) ([]string, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	root, err := readYAMLRoot(s.configFile)
	if err != nil {
		return nil, err
	}
	modelsNode := yamlMapGet(root, "models")
	if modelsNode == nil {
		return nil, fmt.Errorf("models section missing")
	}
	entryNode := yamlMapGet(modelsNode, id)
	if entryNode == nil || entryNode.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("model %q not found", id)
	}

	if req.Cmd != nil {
		yamlMapSet(entryNode, "cmd", yamlScalar(*req.Cmd))
	}
	if req.Name != nil {
		yamlMapSet(entryNode, "name", yamlScalar(*req.Name))
	}
	if req.Description != nil {
		yamlMapSet(entryNode, "description", yamlScalar(*req.Description))
	}
	if req.Aliases != nil {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, a := range *req.Aliases {
			seq.Content = append(seq.Content, yamlScalar(a))
		}
		yamlMapSet(entryNode, "aliases", seq)
	}
	if req.Ttl != nil {
		yamlMapSet(entryNode, "ttl", yamlInt(*req.Ttl))
	}
	// Generated field names: ConcurrencyLimitCamel maps the camelCase JSON key,
	// ConcurrencyLimit maps the snake_case key.
	if req.ConcurrencyLimitCamel != nil {
		if *req.ConcurrencyLimitCamel < 0 {
			return nil, fmt.Errorf("concurrencyLimit must be >= 0")
		}
		yamlMapSet(entryNode, "concurrencyLimit", yamlInt(*req.ConcurrencyLimitCamel))
	}
	if req.ConcurrencyLimit != nil {
		if *req.ConcurrencyLimit < 0 {
			return nil, fmt.Errorf("concurrency_limit must be >= 0")
		}
		yamlMapSet(entryNode, "concurrencyLimit", yamlInt(*req.ConcurrencyLimit))
	}

	flags := make(map[string]string)
	if req.Flags != nil {
		for k, v := range *req.Flags {
			flags[normalizeCmdFlag(k)] = flagValueString(v)
		}
	}
	if req.CtxSize != nil {
		flags["--ctx-size"] = fmt.Sprint(*req.CtxSize)
	}
	if req.CtxSizeDash != nil {
		flags["--ctx-size"] = fmt.Sprint(*req.CtxSizeDash)
	}
	if req.NGpuLayers != nil {
		flags["--n-gpu-layers"] = fmt.Sprint(*req.NGpuLayers)
	}
	if req.NGPULayersDash != nil {
		flags["--n-gpu-layers"] = fmt.Sprint(*req.NGPULayersDash)
	}
	if req.CacheTypeK != nil {
		flags["--cache-type-k"] = *req.CacheTypeK
	}
	if req.CacheTypeKDash != nil {
		flags["--cache-type-k"] = *req.CacheTypeKDash
	}
	if req.CacheTypeV != nil {
		flags["--cache-type-v"] = *req.CacheTypeV
	}
	if req.CacheTypeVDash != nil {
		flags["--cache-type-v"] = *req.CacheTypeVDash
	}
	if len(flags) > 0 {
		cmd := ""
		if n := yamlMapGet(entryNode, "cmd"); n != nil {
			cmd = n.Value
		}
		patched, err := patchCommandFlags(cmd, flags)
		if err != nil {
			return nil, err
		}
		yamlMapSet(entryNode, "cmd", yamlScalar(patched))
	}

	// Translate backend-neutral offload knobs into native flags using the
	// model's configured backend (empty == llamacpp).
	var warnings []string
	if spec := offloadSpecFromPatchRequest(req); !spec.Empty() {
		ops, warn := offload.For(s.cfg.Models[id].Backend).Ops(spec)
		warnings = warn
		if len(ops) > 0 {
			cmd := ""
			if n := yamlMapGet(entryNode, "cmd"); n != nil {
				cmd = n.Value
			}
			patched, err := applyFlagOps(cmd, ops)
			if err != nil {
				return warnings, err
			}
			yamlMapSet(entryNode, "cmd", yamlScalar(patched))
		}
	}

	if err := writeYAMLRoot(s.configFile, root, 0o644); err != nil {
		return warnings, err
	}
	return warnings, nil
}

// patchGroupInConfig applies a partial group update, preserving other fields.
func (s *Server) patchGroupInConfig(id string, req apicontract.ConfigGroupPatchRequest) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	root, err := readYAMLRoot(s.configFile)
	if err != nil {
		return err
	}
	groupsNode := yamlMapGet(root, "groups")
	if groupsNode == nil {
		return fmt.Errorf("groups section missing")
	}
	entryNode := yamlMapGet(groupsNode, id)
	if entryNode == nil || entryNode.Kind != yaml.MappingNode {
		return fmt.Errorf("group %q not found", id)
	}

	if req.AutoUnload != nil {
		yamlMapSet(entryNode, "autoUnload", yamlBool(*req.AutoUnload))
	}
	if req.Exclusive != nil {
		yamlMapSet(entryNode, "exclusive", yamlBool(*req.Exclusive))
	}
	if req.Swap != nil {
		yamlMapSet(entryNode, "swap", yamlBool(*req.Swap))
	}

	return writeYAMLRoot(s.configFile, root, 0o644)
}

// removeModelFromConfig deletes models[id] from the config YAML.
func (s *Server) removeModelFromConfig(id string) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	root, err := readYAMLRoot(s.configFile)
	if err != nil {
		return err
	}
	if modelsNode := yamlMapGet(root, "models"); modelsNode != nil {
		yamlMapDelete(modelsNode, id)
	}
	return writeYAMLRoot(s.configFile, root, 0o644)
}
