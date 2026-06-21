package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/androidand/llama-skein/internal/chain"
	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/logmon"
)

func TestEstimatePromptTokens(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		wantOK bool
		minTok int // 0 = don't assert
		maxTok int
	}{
		{"chat messages", `{"model":"m","messages":[{"role":"user","content":"hello world this is a test"}]}`, true, 1, 50},
		{"empty messages", `{"model":"m","messages":[]}`, false, 0, 0},
		{"legacy prompt", `{"model":"m","prompt":"some prompt text here"}`, true, 1, 50},
		{"no prompt fields", `{"model":"m"}`, false, 0, 0},
		{"garbage", `not json`, false, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := estimatePromptTokens([]byte(tt.body))
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (tokens=%d)", ok, tt.wantOK, got)
			}
			if tt.wantOK && (got < tt.minTok || got > tt.maxTok) {
				t.Errorf("tokens = %d, want in [%d,%d]", got, tt.minTok, tt.maxTok)
			}
		})
	}
}

// newGuardTestServer builds a minimal Server with one model and a seeded
// max_safe_ctx so the guard runs without the fit engine / perf monitor.
func newGuardTestServer(model string, safeCtx int) *Server {
	s := &Server{
		cfg:      config.Config{Models: map[string]config.ModelConfig{model: {}}},
		proxylog: logmon.NewWriter(io.Discard),
	}
	s.maxSafeCtxCache.Store(model, safeCtx)
	return s
}

func guardHandler(s *Server) http.Handler {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		// Echo a marker so the test can confirm the request was forwarded.
		w.Header().Set("X-Forwarded", "1")
		w.WriteHeader(http.StatusOK)
	})
	_ = reached
	return chain.New(s.CreatePromptGuardMiddleware()).Then(next)
}

func TestPromptGuard_RejectsOversizedPrompt(t *testing.T) {
	s := newGuardTestServer("m", 100) // ~100 token safe ceiling

	// A prompt of ~600 chars → ~200 tokens at the 3.0 ratio, over the 100 ceiling.
	big := strings.Repeat("word ", 120)
	body := `{"model":"m","messages":[{"role":"user","content":"` + big + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	guardHandler(s).ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rr.Code)
	}
	if rr.Header().Get("X-Skein-Max-Safe-Ctx") != "100" {
		t.Errorf("X-Skein-Max-Safe-Ctx = %q, want 100", rr.Header().Get("X-Skein-Max-Safe-Ctx"))
	}
	if rr.Header().Get("X-Forwarded") == "1" {
		t.Errorf("request was forwarded; the guard should have rejected it")
	}
	if !strings.Contains(rr.Body.String(), "exceed_context_size_error") {
		t.Errorf("body missing exceed_context_size_error: %s", rr.Body.String())
	}
}

func TestPromptGuard_AllowsSmallPrompt(t *testing.T) {
	s := newGuardTestServer("m", 100000)
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	guardHandler(s).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK || rr.Header().Get("X-Forwarded") != "1" {
		t.Fatalf("small prompt should be forwarded; status=%d forwarded=%q", rr.Code, rr.Header().Get("X-Forwarded"))
	}
}

func TestPromptGuard_SkipsWhenMaxSafeCtxUnknown(t *testing.T) {
	s := newGuardTestServer("m", 0) // 0 = unknown → guard disabled
	big := strings.Repeat("word ", 5000)
	body := `{"model":"m","messages":[{"role":"user","content":"` + big + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	guardHandler(s).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK || rr.Header().Get("X-Forwarded") != "1" {
		t.Fatalf("unknown max_safe_ctx must not reject; status=%d forwarded=%q", rr.Code, rr.Header().Get("X-Forwarded"))
	}
}
