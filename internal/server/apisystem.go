package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/androidand/llama-skein/internal/event"
	"github.com/androidand/llama-skein/internal/router"
	"github.com/androidand/llama-skein/internal/shared"
)

const systemCapabilitiesVersion = 1

// handleAPISystemVersion implements GET /api/system/version.
func (s *Server) handleAPISystemVersion(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"version":       s.build.SkeinVersion,
		"commit":        s.build.Commit,
		"build_date":    s.build.Date,
		"upstream":      map[string]string{"llama_skein_version": s.build.UpstreamVersion},
		"skein_version": s.build.SkeinVersion,
		"runtime": map[string]string{
			"go_os":   goOS(),
			"go_arch": goArch(),
		},
	}
	if s.build.LlamaCppBuild != "" && s.build.LlamaCppBuild != "unknown" {
		var features []string
		for _, f := range splitFeatures(s.build.BuildFeatures) {
			if f != "" {
				features = append(features, f)
			}
		}
		resp["llama_cpp_build"] = s.build.LlamaCppBuild
		resp["llama_cpp_git"] = s.build.LlamaCppGit
		resp["llama_cpp_date"] = s.build.LlamaCppDate
		resp["build_features"] = features
		resp["platform"] = goOS() + "/" + goArch()
		resp["metal"] = strings.Contains(strings.ToLower(s.build.BuildFeatures), "metal") ||
			(goOS() == "darwin" && goArch() == "arm64")
		if strings.Contains(strings.ToLower(s.build.BuildFeatures), "rocm") {
			if v := detectRocmVersion(); v != "" {
				resp["rocm_version"] = v
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleAPISystemCapabilities implements GET /api/system/capabilities.
// Returns the feature set supported by this instance; clients check this to
// detect optional features without relying on 404 probing.
func (s *Server) handleAPISystemCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"version":  systemCapabilitiesVersion,
		"features": currentSystemFeatures,
	})
}

// currentSystemFeatures is the authoritative list of optional capabilities
// this server exposes beyond the base OpenAI-compatible inference API.
var currentSystemFeatures = []string{
	"capabilities",
	"hardware-power",
	"llama-cpp-upgrade",
}

// handleAPISystemMetrics implements GET /api/system/metrics.
// Serves the in-app request activity log as a JSON array.
func (s *Server) handleAPISystemMetrics(w http.ResponseWriter, r *http.Request) {
	data, err := s.metrics.getMetricsJSON()
	if err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError, "failed to get metrics")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// handleAPISystemCaptures implements GET /api/system/captures/{id}.
// Returns the stored request/response capture for the given metric entry ID.
func (s *Server) handleAPISystemCaptures(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		router.SendResponse(w, r, http.StatusBadRequest, "invalid capture ID")
		return
	}
	capture := s.metrics.getCaptureByID(id)
	if capture == nil {
		router.SendResponse(w, r, http.StatusNotFound, "capture not found")
		return
	}
	jsonBytes, err := json.Marshal(capture)
	if err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError, "failed to marshal capture")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(jsonBytes)
}

// handleAPISystemUpgrade implements POST /api/system/upgrade.
// Delegates to the upgrade logic in apiupgrade.go.
func (s *Server) handleAPISystemUpgrade(w http.ResponseWriter, r *http.Request) {
	s.runUpgrade(w, r)
}

// handleAPISystemProvider implements GET /api/system/provider.
// Returns provider identity and deployment metadata for agent discovery.
func (s *Server) handleAPISystemProvider(w http.ResponseWriter, r *http.Request) {
	hostname, _ := osHostname()
	ip := detectLocalIP()
	resp := map[string]any{
		"provider": map[string]any{
			"name":       providerName(hostname),
			"hostname":   hostname,
			"ip":         ip,
			"os":         goOS(),
			"arch":       goArch(),
			"ssh_method": sshMethod(hostname),
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// osHostname returns the OS hostname.
func osHostname() (string, error) {
	return os.Hostname()
}

// detectLocalIP returns the first non-loopback IPv4 address.
func detectLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "unknown"
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "unknown"
}

// providerName derives a short provider name from the hostname.
func providerName(hostname string) string {
	switch {
	case strings.Contains(hostname, "rocky"):
		return "rocky"
	case strings.Contains(hostname, "llama-skein"):
		return "proxmox"
	case strings.Contains(hostname, "m3"):
		return "m3"
	case strings.Contains(hostname, "m5"):
		return "m5"
	default:
		return hostname
	}
}

// sshMethod returns the SSH deployment method based on hostname.
func sshMethod(hostname string) string {
	switch {
	case strings.Contains(hostname, "rocky"):
		return "direct"
	case strings.Contains(hostname, "llama-skein"):
		return "lxc_mount"
	default:
		return "scp"
	}
}

// --- SSE event stream ---

type messageType string

const (
	msgTypeModelStatus messageType = "modelStatus"
	msgTypeLogData     messageType = "logData"
	msgTypeMetrics     messageType = "metrics"
	msgTypeInFlight    messageType = "inflight"
)

type messageEnvelope struct {
	Type messageType `json:"type"`
	Data string      `json:"data"`
}

// apiModel is one entry in the modelStatus SSE payload.
type apiModel struct {
	Id          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	Unlisted    bool     `json:"unlisted"`
	PeerID      string   `json:"peerID"`
	Aliases     []string `json:"aliases,omitempty"`
}

// modelStatus returns every configured model with its current process state.
func (s *Server) modelStatus() []apiModel {
	running := s.local.RunningModels()

	ids := make([]string, 0, len(s.cfg.Models))
	for id := range s.cfg.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	models := make([]apiModel, 0, len(ids))
	for _, id := range ids {
		mc := s.cfg.Models[id]
		state := "stopped"
		if st, ok := running[id]; ok {
			state = string(st)
		}
		models = append(models, apiModel{
			Id:          id,
			Name:        mc.Name,
			Description: mc.Description,
			State:       state,
			Unlisted:    mc.Unlisted,
			Aliases:     mc.Aliases,
		})
	}
	for peerID, peer := range s.cfg.Peers {
		for _, modelID := range peer.Models {
			models = append(models, apiModel{Id: modelID, PeerID: peerID})
		}
	}
	return models
}

// handleAPISystemEvents implements GET /api/system/events.
// Streams server-sent events: model state changes, log data, metrics, inflight counts.
func (s *Server) handleAPISystemEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		router.SendResponse(w, r, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	sendBuffer := make(chan messageEnvelope, 1024)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	send := func(msg messageEnvelope) {
		select {
		case sendBuffer <- msg:
		case <-ctx.Done():
			s.proxylog.Warn("handleAPISystemEvents send suppressed due to context done")
		default:
			s.proxylog.Warn("handleAPISystemEvents sendBuffer full, dropped message")
		}
	}
	sendModels := func() {
		if data, err := json.Marshal(s.modelStatus()); err == nil {
			send(messageEnvelope{Type: msgTypeModelStatus, Data: string(data)})
		}
	}
	sendLogData := func(source string, data []byte) {
		if j, err := json.Marshal(map[string]string{"source": source, "data": string(data)}); err == nil {
			send(messageEnvelope{Type: msgTypeLogData, Data: string(j)})
		}
	}
	sendMetrics := func(metrics []ActivityLogEntry) {
		if j, err := json.Marshal(metrics); err == nil {
			send(messageEnvelope{Type: msgTypeMetrics, Data: string(j)})
		}
	}
	sendInFlight := func(total int) {
		if j, err := json.Marshal(map[string]int{"total": total}); err == nil {
			send(messageEnvelope{Type: msgTypeInFlight, Data: string(j)})
		}
	}

	defer event.On(func(e shared.ProcessStateChangeEvent) { sendModels() })()
	defer event.On(func(e shared.ConfigFileChangedEvent) { sendModels() })()
	defer s.proxylog.OnLogData(func(data []byte) { sendLogData("proxy", data) })()
	defer s.upstreamlog.OnLogData(func(data []byte) { sendLogData("upstream", data) })()
	defer event.On(func(e ActivityLogEvent) { sendMetrics([]ActivityLogEntry{e.Metrics}) })()
	defer event.On(func(e shared.InFlightRequestsEvent) { sendInFlight(e.Total) })()

	sendLogData("proxy", s.proxylog.GetHistory())
	sendLogData("upstream", s.upstreamlog.GetHistory())
	sendModels()
	sendMetrics(s.metrics.getMetrics())
	sendInFlight(int(s.inflight.Current()))

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.shutdownCtx.Done():
			return
		case msg := <-sendBuffer:
			data, err := json.Marshal(msg)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event:message\ndata:%s\n\n", data)
			flusher.Flush()
		}
	}
}
