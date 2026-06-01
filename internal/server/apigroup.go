package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/androidand/llama-skein/internal/router"
)

// handleAPIUnloadAll implements POST /api/models/unload.
// Stops every running local process immediately.
func (s *Server) handleAPIUnloadAll(w http.ResponseWriter, r *http.Request) {
	s.local.Unload(0)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"msg": "ok"})
}

// handleAPIUnloadModel implements POST /api/models/unload/{model}.
// Stops a single named local process.
func (s *Server) handleAPIUnloadModel(w http.ResponseWriter, r *http.Request) {
	requested := strings.TrimPrefix(r.PathValue("model"), "/")
	realName, found := s.cfg.RealModelName(requested)
	if !found {
		router.SendResponse(w, r, http.StatusNotFound, "model not found")
		return
	}
	if !s.local.Handles(realName) {
		router.SendResponse(w, r, http.StatusNotFound, "no local server found for requested model")
		return
	}
	s.local.Unload(0, realName)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}
