package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/androidand/llama-skein/internal/chain"
	"github.com/androidand/llama-skein/internal/router"
	"github.com/androidand/llama-skein/pkg/apicontract"
)

// minViableCtx is the smallest context worth clamping to. If a model does not
// fit even at this context, its weights (not the KV cache) are the problem —
// shrinking ctx can't help, so the model is marked unfittable and refused.
const minViableCtx = 2048

// clampModelsToFit is the proactive half of the fit guard. Before the router
// captures each model's config and before preload runs, it consults the fit
// engine and, for any model that would NOT fit this host's memory at its
// configured context:
//   - shrinks --ctx-size to the largest safe context, if one exists (the
//     KV cache was the problem: "refuse + shrink first"); or
//   - records it as unfittable when even a minimal context won't fit (the
//     weights exceed memory) so the load path refuses it instead of OOM-ing.
//
// It FAILS OPEN: a model whose fit cannot be sized confidently (VRAM telemetry
// still warming up, un-parseable weights, non-modeled backend) is left exactly
// as configured. It never expands a context or blocks a model it cannot size.
// Must run after s.cfg and s.perf are set but before the router is built.
func (s *Server) clampModelsToFit() {
	if s.unfittable == nil {
		s.unfittable = map[string]string{}
	}
	for id := range s.cfg.Models {
		mf, ok := s.fitForModel(id)
		if !ok || !s.confidentNoFit(mf) {
			continue // fits, unknown, or un-sizable → leave alone (fail open)
		}
		mc := s.cfg.Models[id]
		if mf.MaxSafeCtx >= minViableCtx {
			if newCmd := setCtxSizeInCmd(mc.Cmd, mf.MaxSafeCtx); newCmd != mc.Cmd {
				mc.Cmd = newCmd
				s.cfg.Models[id] = mc
				s.proxylog.Warnf("fit-guard: model %q won't fit at its configured context; clamped --ctx-size to %d to avoid an OOM on load", id, mf.MaxSafeCtx)
				continue
			}
		}
		reason := "model weights exceed this host's available memory; loading it would OOM the host"
		if mf.Reason != nil && *mf.Reason != "" {
			reason = *mf.Reason
		}
		s.unfittable[id] = reason
		s.proxylog.Warnf("fit-guard: model %q cannot fit host memory even at minimal context — it will be refused rather than loaded. %s", id, reason)
	}
	// Contexts may have changed; drop the pre-flight cache so the prompt guard
	// re-derives max_safe_ctx from the clamped commands.
	s.maxSafeCtxCache.Range(func(k, _ any) bool { s.maxSafeCtxCache.Delete(k); return true })
}

// confidentNoFit reports whether a ModelFit is a trustworthy "does not fit"
// verdict — FitLevel "no" backed by a known host VRAM figure and a known model
// weight size. A "no" without those (missing metadata, VRAM not yet available)
// is treated as "don't know", never as "won't fit".
func (s *Server) confidentNoFit(mf apicontract.ModelFit) bool {
	return mf.FitLevel == apicontract.No &&
		mf.VramTotalMb != nil && *mf.VramTotalMb > 0 &&
		mf.ModelMb != nil && *mf.ModelMb > 0
}

// modelLoadRefusal returns a reason and true when loading modelID now would not
// fit host memory. Fail-open: only a confident "won't fit" verdict refuses.
func (s *Server) modelLoadRefusal(id string) (string, bool) {
	if r, ok := s.unfittable[id]; ok {
		return r, true
	}
	mf, ok := s.fitForModel(id)
	if !ok || !s.confidentNoFit(mf) {
		return "", false
	}
	reason := "model does not fit this host's available memory"
	if mf.Reason != nil && *mf.Reason != "" {
		reason = *mf.Reason
	}
	return reason, true
}

// CreateLoadFitGateMiddleware refuses to load a model that would not fit this
// host's memory, BEFORE the request reaches the router (which would launch the
// backend and OOM-crash the host — fatal on unified-memory Macs). It only acts
// when the model is not already resident (a loaded model already fit) and when
// the fit verdict is confident. Returns 507 so the client can pick another
// host/model instead of taking the box down. Preload is guarded separately in
// startPreload (it bypasses this HTTP chain).
func (s *Server) CreateLoadFitGateMiddleware() chain.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, err := router.FetchContext(r, s.cfg)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			if _, loaded := s.modelState(data.ModelID); loaded {
				next.ServeHTTP(w, r)
				return
			}
			reason, refuse := s.modelLoadRefusal(data.ModelID)
			if !refuse {
				next.ServeHTTP(w, r)
				return
			}
			s.proxylog.Warnf("<%s> fit-guard: refusing to load — %s", data.ModelID, reason)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInsufficientStorage) // 507
			_ = json.NewEncoder(w).Encode(modelWontFitError(data.ModelID, reason))
		})
	}
}

func modelWontFitError(model, reason string) any {
	type errBody struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	}
	return struct {
		Error errBody `json:"error"`
	}{Error: errBody{
		Message: fmt.Sprintf("model %q will not fit this host and was not loaded: %s", model, reason),
		Type:    "model_does_not_fit_error",
		Code:    "model_over_host_memory",
	}}
}

// setCtxSizeInCmd rewrites the --ctx-size / -c value in a launch command to n,
// handling the bare ("--ctx-size N") and "=" ("--ctx-size=N") forms. If no
// context flag is present the command is returned unchanged (the backend's
// default context is presumed to already fit — the fit engine sized it).
func setCtxSizeInCmd(cmd string, n int) string {
	tokens := strings.Fields(cmd)
	val := strconv.Itoa(n)
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		switch {
		case tok == "--ctx-size" || tok == "-c":
			if i+1 < len(tokens) {
				tokens[i+1] = val
				return strings.Join(tokens, " ")
			}
		case strings.HasPrefix(tok, "--ctx-size="):
			tokens[i] = "--ctx-size=" + val
			return strings.Join(tokens, " ")
		case strings.HasPrefix(tok, "-c="):
			tokens[i] = "-c=" + val
			return strings.Join(tokens, " ")
		}
	}
	return cmd
}
