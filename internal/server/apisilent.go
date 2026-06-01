package server

import (
	"encoding/json"
	"net/http"

	"github.com/androidand/llama-skein/internal/thermal"
)

// handleAPISkeinSilentGet implements GET /api/skein/silent.
func (s *Server) handleAPISkeinSilentGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.silentMode.GetState())
}

// handleAPISkeinSilentEnable implements POST /api/skein/silent.
// Body is optional: { "power_limit_pct": 65, "temp_target_celsius": 82 }
func (s *Server) handleAPISkeinSilentEnable(w http.ResponseWriter, r *http.Request) {
	profile := thermal.DefaultSilentProfile
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&profile)
	}
	if err := s.silentMode.Apply(profile); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, s.silentMode.GetState())
}

// handleAPISkeinSilentDisable implements DELETE /api/skein/silent.
func (s *Server) handleAPISkeinSilentDisable(w http.ResponseWriter, r *http.Request) {
	if err := s.silentMode.Restore(); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, s.silentMode.GetState())
}
