package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/androidand/llama-skein/internal/chain"
	"github.com/androidand/llama-skein/internal/router"
)

// bytesPerTokenEstimate is the conservative chars-per-token ratio used to size a
// prompt without a tokenizer. Real ratios are ~4 chars/token for English and
// ~3 for code; 3.0 over-estimates so the guard fails safe (rejects slightly
// early rather than letting an OOM-crashing prompt through). The fit engine's
// max_safe_ctx already carries an ~8% margin on top of this.
const bytesPerTokenEstimate = 3.0

// maxSafeCtx returns the memoized fit max_safe_ctx for a real model id, or 0
// when it cannot be computed (unknown model, un-analyzable backend, VRAM not
// ready). 0 disables the guard for that model — never reject what we can't size.
func (s *Server) maxSafeCtx(realName string) int {
	if v, ok := s.maxSafeCtxCache.Load(realName); ok {
		return v.(int)
	}
	n := 0
	if mf, ok := s.fitForModel(realName); ok {
		n = mf.MaxSafeCtx
	}
	s.maxSafeCtxCache.Store(realName, n)
	return n
}

// CreatePromptGuardMiddleware rejects a chat/completions request whose estimated
// prompt size exceeds the model's max_safe_ctx, BEFORE forwarding it to a
// backend that would OOM-crash (MLX) or 413 late (llama.cpp). It returns 413
// with X-Skein-Max-Safe-Ctx and an exceed_context_size_error body so the client
// (opencode-skein) can trim to the advertised size and retry. The guard is a
// no-op for non-JSON bodies and for models with an unknown max_safe_ctx.
func (s *Server) CreatePromptGuardMiddleware() chain.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.Body == nil ||
				!strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
				next.ServeHTTP(w, r)
				return
			}

			data, err := router.FetchContext(r, s.cfg)
			if err != nil {
				next.ServeHTTP(w, r) // routing layer reports the missing model
				return
			}
			safe := s.maxSafeCtx(data.ModelID)
			if safe <= 0 {
				next.ServeHTTP(w, r) // can't size this model; don't guard it
				return
			}

			body, err := io.ReadAll(r.Body)
			r.Body.Close()
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			// Always restore the body so downstream filters/forwarding are
			// unaffected on the allowed path.
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))

			tokens, ok := estimatePromptTokens(body)
			if !ok || tokens <= safe {
				next.ServeHTTP(w, r)
				return
			}

			s.proxylog.Warnf("<%s> pre-flight: prompt ~%d tokens exceeds max_safe_ctx %d — rejecting with 413 (would OOM/413 the backend)", data.ModelID, tokens, safe)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Skein-Max-Safe-Ctx", strconv.Itoa(safe))
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_ = json.NewEncoder(w).Encode(promptOverCtxError(data.ModelID, tokens, safe))
		})
	}
}

func promptOverCtxError(model string, tokens, safe int) any {
	return struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}{Error: struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	}{
		Message: fmt.Sprintf("prompt (~%d tokens) exceeds the safe context for model %q on this host (max_safe_ctx %d); trim the prompt and retry", tokens, model, safe),
		Type:    "exceed_context_size_error",
		Code:    "prompt_over_max_safe_ctx",
	}}
}

// estimatePromptTokens approximates the prompt token count of an OpenAI
// chat/completions or completions request body. It sums message content, the
// (legacy) prompt field, and a small per-message formatting overhead, then
// converts characters to tokens with a conservative ratio. ok is false when the
// body has no recognizable prompt to size.
func estimatePromptTokens(body []byte) (tokens int, ok bool) {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Prompt json.RawMessage `json:"prompt"`
	}
	if json.Unmarshal(body, &req) != nil {
		return 0, false
	}
	chars := 0
	for _, m := range req.Messages {
		chars += len(m.Content) + len(m.Role) + 8 // +8: role/turn formatting tokens
	}
	if n := len(req.Prompt); n > 2 { // skip JSON null/empty
		chars += n
	}
	if chars == 0 {
		return 0, false
	}
	return int(float64(chars) / bytesPerTokenEstimate), true
}
