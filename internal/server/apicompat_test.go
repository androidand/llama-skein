package server

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/logmon"
)

// TestServer_LegacyCompatRoutesRegistered guards against the legacy fork
// paths disappearing again: skein consumes /api/events, /api/resources,
// /api/storage, /api/ps and probes /api/tags for provider detection.
func TestServer_LegacyCompatRoutesRegistered(t *testing.T) {
	logger := logmon.NewWriter(io.Discard)
	s, err := New(config.Config{}, logger, logger, logger, nil, BuildInfo{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { s.Shutdown(0) })

	cases := []struct {
		method, path string
		body         string
	}{
		{"GET", "/api/resources", ""},
		{"GET", "/api/storage", ""},
		{"GET", "/api/ps", ""},
		{"GET", "/api/tags", ""},
		{"POST", "/api/show", `{"model":"nope"}`},
		{"DELETE", "/api/delete", `{"model":"nope"}`},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		rr := httptest.NewRecorder()
		s.ServeHTTP(rr, req)
		if rr.Code == 404 && !strings.Contains(rr.Body.String(), "model not found") {
			t.Errorf("%s %s: route not registered (404: %s)", c.method, c.path, rr.Body.String())
		}
	}

	// /api/tags must return the Ollama envelope.
	req := httptest.NewRequest("GET", "/api/tags", nil)
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"models"`) {
		t.Errorf("GET /api/tags: expected 200 with models envelope, got %d: %s", rr.Code, rr.Body.String())
	}
}
