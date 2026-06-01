package server

import "net/http"

const skeinCapabilitiesVersion = 1

type skeinCapabilities struct {
	Version  int      `json:"version"`
	Features []string `json:"features"`
}

var currentSkeinFeatures = []string{
	"capabilities",
	"silent-mode",
}

// handleAPISkeinCapabilities implements GET /api/skein/capabilities.
// Returns the list of companion features supported by this instance.
// Skein detects 404 here and falls back to plain OpenAI mode.
func (s *Server) handleAPISkeinCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, skeinCapabilities{
		Version:  skeinCapabilitiesVersion,
		Features: currentSkeinFeatures,
	})
}
