package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/event"
	"github.com/androidand/llama-skein/internal/router"
	"github.com/androidand/llama-skein/internal/shared"
	"github.com/androidand/llama-skein/pkg/apicontract"
)

// handleListModels serves the OpenAI-compatible model listing: local models
// (with optional aliases) plus peer models.
func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	created := int(time.Now().Unix())
	data := make([]apicontract.Model, 0, len(s.cfg.Models))
	defaultID, _ := s.cfg.RealModelName(s.cfg.DefaultModel)

	newRecord := func(id, name, description string, mc config.ModelConfig, state string, loaded bool) apicontract.Model {
		rec := apicontract.Model{
			Id:      id,
			Object:  "model",
			Created: &created,
			OwnedBy: stringPtr("llama-skein"),
		}
		if defaultID != "" && id == defaultID {
			isDefault := true
			rec.Default = &isDefault
		}
		if name = strings.TrimSpace(name); name != "" {
			rec.Name = &name
		}
		if description = strings.TrimSpace(description); description != "" {
			rec.Description = &description
		}
		if state != "" {
			rec.State = &state
			rec.Loaded = &loaded
		}
		hints := map[string]any{}
		addModelRuntimeHints(hints, mc)
		if contextLength, ok := hints["context_length"].(int); ok {
			rec.ContextLength = &contextLength
		}
		if maxOutputTokens, ok := hints["max_output_tokens"].(int); ok {
			rec.MaxOutputTokens = &maxOutputTokens
		}
		if v, ok := hints["n_cpu_moe"].(int); ok {
			rec.NCpuMoe = &v
		}
		if v, ok := hints["cpu_moe"].(bool); ok {
			rec.CpuMoe = &v
		}
		if v, ok := hints["cpu_offload_gb"].(int); ok {
			rec.CpuOffloadGb = &v
		}
		if v, ok := hints["override_tensor"].(string); ok {
			rec.OverrideTensor = &v
		}
		return rec
	}

	for id, mc := range s.cfg.Models {
		if mc.Unlisted {
			continue
		}
		state, loaded := s.modelState(id)
		data = append(data, newRecord(id, mc.Name, mc.Description, mc, state, loaded))

		if s.cfg.IncludeAliasesInList {
			for _, alias := range mc.Aliases {
				if alias := strings.TrimSpace(alias); alias != "" {
					data = append(data, newRecord(alias, mc.Name, mc.Description, mc, state, loaded))
				}
			}
		}
	}

	for peerID, peer := range s.cfg.Peers {
		for _, modelID := range peer.Models {
			data = append(data, newRecord(modelID, peerID+": "+modelID, "", config.ModelConfig{}, "", false))
		}
	}

	// Alphabetical, except the default model is listed first: some clients
	// pick the first entry when the user has not chosen a model.
	sort.Slice(data, func(i, j int) bool {
		di := data[i].Default != nil && *data[i].Default
		dj := data[j].Default != nil && *data[j].Default
		if di != dj {
			return di
		}
		return data[i].Id < data[j].Id
	})

	// Echo the Origin so browser clients can read the listing.
	if origin := r.Header.Get("Origin"); origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apicontract.ModelList{
		Object: apicontract.List,
		Data:   data,
	})
}

func stringPtr(value string) *string {
	return &value
}

// runningModel is one entry in the /running listing.
type runningModel struct {
	Model       string `json:"model"`
	State       string `json:"state"`
	Cmd         string `json:"cmd"`
	Proxy       string `json:"proxy"`
	TTL         int    `json:"ttl"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// handleUnload stops every running local process. Peer models are remote and
// unaffected.
func (s *Server) handleUnload(w http.ResponseWriter, r *http.Request) {
	s.local.Unload(0)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// handleRunning lists local processes that are not stopped, joining each model
// ID against its config for the cmd/proxy/ttl/name/description metadata.
func (s *Server) handleRunning(w http.ResponseWriter, r *http.Request) {
	states := s.local.RunningModels()
	list := make([]runningModel, 0, len(states))
	for id, state := range states {
		mc := s.cfg.Models[id]
		list = append(list, runningModel{
			Model:       id,
			State:       string(state),
			Cmd:         mc.Cmd,
			Proxy:       mc.Proxy,
			TTL:         mc.UnloadAfter,
			Name:        mc.Name,
			Description: mc.Description,
		})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Model < list[j].Model })

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"running": list})
}

// discardResponseWriter satisfies http.ResponseWriter for preload requests,
// dropping the body while capturing the status code.
type discardResponseWriter struct {
	header http.Header
	status int
}

func (d *discardResponseWriter) Header() http.Header {
	if d.header == nil {
		d.header = make(http.Header)
	}
	return d.header
}

func (d *discardResponseWriter) Write(p []byte) (int, error) { return len(p), nil }

func (d *discardResponseWriter) WriteHeader(status int) { d.status = status }

// startPreload fires a background GET / at every model named in
// Hooks.OnStartup.Preload so they are warm before the first real request.
// Preload names are already resolved to real model IDs by config loading.
func (s *Server) startPreload() {
	models := s.cfg.Hooks.OnStartup.Preload
	if len(models) == 0 {
		return
	}
	go func() {
		for _, modelID := range models {
			if !s.local.Handles(modelID) {
				s.proxylog.Warnf("preload: model %s is not a local model, skipping", modelID)
				continue
			}
			s.proxylog.Infof("preloading model: %s", modelID)

			req, err := http.NewRequestWithContext(s.shutdownCtx, http.MethodGet, "/", nil)
			if err != nil {
				continue
			}
			req = req.WithContext(router.SetContext(req.Context(), router.ReqContextData{Model: modelID, ModelID: modelID}))

			dw := &discardResponseWriter{status: http.StatusOK}
			s.local.ServeHTTP(dw, req)

			success := dw.status < http.StatusBadRequest
			if !success {
				s.proxylog.Errorf("failed to preload model %s: status %d", modelID, dw.status)
			}
			event.Emit(shared.ModelPreloadedEvent{ModelName: modelID, Success: success})
		}
	}()
}

// handleMetrics serves Prometheus-format performance metrics. Returns 503 when
// performance monitoring is disabled.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.perf == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("# performance monitor not available\n"))
		return
	}
	s.perf.MetricsHandler().ServeHTTP(w, r)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func handleRootRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui", http.StatusFound)
}

func handleUpstreamRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/models", http.StatusFound)
}

// handleUpstream proxies ANY request under /upstream/<model>/<path> directly to
// the model's process, bypassing model dispatch by body/query inspection.
func (s *Server) handleUpstream(w http.ResponseWriter, r *http.Request) {
	upstreamPath := r.PathValue("upstreamPath")

	searchName, modelID, remainingPath, found := findModelInPath(s.cfg, "/"+upstreamPath)
	if !found {
		router.SendResponse(w, r, http.StatusNotFound, "model not found")
		return
	}

	// Redirect /upstream/model to /upstream/model/ so relative URLs in upstream
	// responses resolve. 301 for GET/HEAD, 308 otherwise to preserve the method.
	if remainingPath == "/" && !strings.HasSuffix(r.URL.Path, "/") {
		newPath := "/upstream/" + searchName + "/"
		if r.URL.RawQuery != "" {
			newPath += "?" + r.URL.RawQuery
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			http.Redirect(w, r, newPath, http.StatusMovedPermanently)
		} else {
			http.Redirect(w, r, newPath, http.StatusPermanentRedirect)
		}
		return
	}

	// Strip the /upstream/<model> prefix before forwarding.
	r.URL.Path = remainingPath
	// Pin the resolved model so the router skips body/query extraction.
	*r = *r.WithContext(router.SetContext(r.Context(), router.ReqContextData{Model: searchName, ModelID: modelID}))

	switch {
	case s.local.Handles(modelID):
		s.local.ServeHTTP(w, r)
	case s.peer.Handles(modelID):
		s.peer.ServeHTTP(w, r)
	default:
		router.SendResponse(w, r, http.StatusNotFound, "no router for model "+modelID)
	}
}

// findModelInPath walks a slash-separated path, building up segments until one
// matches a configured model. This resolves model names that contain slashes
// (e.g. "author/model"). Returns the matched name, its real model ID, the
// remaining path, and whether a match was found.
func findModelInPath(cfg config.Config, path string) (searchName, realName, remainingPath string, found bool) {
	parts := strings.Split(strings.TrimSpace(path), "/")
	name := ""

	for i, part := range parts {
		if part == "" {
			continue
		}
		if name == "" {
			name = part
		} else {
			name = name + "/" + part
		}

		if modelID, ok := cfg.RealModelName(name); ok {
			return name, modelID, "/" + strings.Join(parts[i+1:], "/"), true
		}
	}

	return "", "", "", false
}
