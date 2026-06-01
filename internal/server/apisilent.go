package server

import (
	"encoding/json"
	"net/http"

	"github.com/androidand/llama-skein/internal/thermal"
)

// handleAPISkeinSilentGet implements GET /api/skein/silent.
// Always returns 200; callers check the "available" field to discover HW support.
func (s *Server) handleAPISkeinSilentGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.silentMode.GetState())
}

// handleAPISkeinSilentEnable implements POST /api/skein/silent.
// Body is optional: { "power_limit_pct": 65, "temp_target_celsius": 82 }
// Returns 503 when GPU power control is unavailable, 500 on unexpected HW error.
func (s *Server) handleAPISkeinSilentEnable(w http.ResponseWriter, r *http.Request) {
	state := s.silentMode.GetState()
	if !state.Available {
		silentError(w, http.StatusServiceUnavailable, state.UnavailableReason)
		return
	}
	profile := thermal.DefaultSilentProfile
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&profile)
	}
	if err := s.silentMode.Apply(profile); err != nil {
		silentError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, s.silentMode.GetState())
}

// handleAPISkeinSilentDisable implements DELETE /api/skein/silent.
// Returns 503 when GPU power control is unavailable, 500 on unexpected HW error.
func (s *Server) handleAPISkeinSilentDisable(w http.ResponseWriter, r *http.Request) {
	state := s.silentMode.GetState()
	if !state.Available {
		silentError(w, http.StatusServiceUnavailable, state.UnavailableReason)
		return
	}
	if err := s.silentMode.Restore(); err != nil {
		silentError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, s.silentMode.GetState())
}

func silentError(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": reason})
}
