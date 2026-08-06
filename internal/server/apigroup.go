package server

import (
	"net/http"
	"sort"
	"strings"

	"github.com/androidand/llama-skein/internal/router"
)

// handleAPIUnloadAll implements POST /api/models/unload.
// Stops every running local process immediately.
func (s *Server) handleAPIUnloadAll(w http.ResponseWriter, r *http.Request) {
	s.local.Unload(0)
	// task 5.2: Unload's own doc comment (internal/router/base.go) promises
	// callers stay blocked "until each targeted process has actually
	// exited" — this check is a defensive confirmation of that guarantee,
	// not a routine outcome. Reported as an additive field, never changing
	// the pre-existing "msg":"ok" success shape any caller may already
	// depend on.
	body := map[string]any{"msg": "ok"}
	if still := s.local.RunningModels(); len(still) > 0 {
		ids := make([]string, 0, len(still))
		for id := range still {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		body["still_running"] = ids
	}
	writeJSONStatus(w, http.StatusOK, body)
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

	// task 5.2: same defensive confirmation as handleAPIUnloadAll — Unload
	// blocks until the process has actually exited, so this branch is not
	// expected to fire; it exists so a violation of that guarantee is a
	// real, observable 500 instead of a silent "OK" lie, the same category
	// of bug handleAPILoadModel had before this task.
	if state, loaded := s.modelState(realName); loaded {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{
			"model": realName,
			"state": state,
			"error": "model is still reported as loaded after unload returned",
		})
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}
