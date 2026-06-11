package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/logmon"
	"github.com/androidand/llama-skein/internal/router"
	"github.com/androidand/llama-skein/pkg/apicontract"
)

// echoRouter echoes the forwarded request body so tests can observe rewrites.
type echoRouter struct {
	stubRouter
}

func (e *echoRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

// newTestServerWithConfig wires a Server like newTestServer but with cfg set
// before routes() so middleware closures capture it.
func newTestServerWithConfig(cfg config.Config, local router.LocalRouter, peer router.Router) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	proxylog := logmon.NewWriter(io.Discard)
	s := &Server{
		cfg:         cfg,
		muxlog:      logmon.NewWriter(io.Discard),
		proxylog:    proxylog,
		upstreamlog: logmon.NewWriter(io.Discard),
		inflight:    &inflightCounter{},
		metrics:     newMetricsMonitor(proxylog, 0, 0),
		local:       local,
		peer:        peer,
		shutdownCtx: ctx,
		shutdownFn:  cancel,
	}
	s.routes()
	return s
}

func TestServer_DefaultModel_DispatchAndBodyInjection(t *testing.T) {
	cfg := config.Config{
		Models:       map[string]config.ModelConfig{"m1": {}},
		DefaultModel: "m1",
	}
	local := &echoRouter{stubRouter: *newStubRouter([]string{"m1"}, "")}
	s := newTestServerWithConfig(cfg, local, newStubRouter(nil, ""))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	var forwarded struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &forwarded); err != nil {
		t.Fatalf("decode forwarded body: %v body=%q", err, w.Body.String())
	}
	if forwarded.Model != "m1" {
		t.Errorf("forwarded model=%q want %q (default injected into body)", forwarded.Model, "m1")
	}
}

func TestServer_DefaultModel_MissingModelWithoutDefault404(t *testing.T) {
	s := newTestServerWithConfig(
		config.Config{Models: map[string]config.ModelConfig{"m1": {}}},
		newStubRouter([]string{"m1"}, ""), newStubRouter(nil, ""),
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 body=%q", w.Code, w.Body.String())
	}
}

func TestServer_ListModels_DefaultFirst(t *testing.T) {
	cfg := config.Config{
		Models: map[string]config.ModelConfig{
			"a-model": {},
			"z-model": {},
		},
		DefaultModel: "z-model",
	}
	s := newTestServerWithConfig(cfg, newStubRouter(nil, ""), newStubRouter(nil, ""))

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	var list apicontract.ModelList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Data) != 2 {
		t.Fatalf("models=%d want 2", len(list.Data))
	}
	if list.Data[0].Id != "z-model" {
		t.Errorf("first model=%q want z-model (default sorts first)", list.Data[0].Id)
	}
	if list.Data[0].Default == nil || !*list.Data[0].Default {
		t.Error("default model not marked with default=true")
	}
	if list.Data[1].Default != nil {
		t.Error("non-default model should omit the default field")
	}
}

func TestServer_APIDefaultModel_GetSetClear(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configFile, []byte(`
defaultModel: m1
models:
  m1:
    cmd: server-one
  m2:
    cmd: server-two
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		Models:       map[string]config.ModelConfig{"m1": {}, "m2": {}},
		DefaultModel: "m1",
	}
	s := newTestServerWithConfig(cfg, newStubRouter(nil, ""), newStubRouter(nil, ""))
	s.SetConfigFile(configFile)

	do := func(method, body string) *httptest.ResponseRecorder {
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, "/api/config/default-model", rdr)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w
	}

	// GET returns the configured default.
	w := do(http.MethodGet, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"model":"m1"`) {
		t.Errorf("GET status=%d body=%q want model m1", w.Code, w.Body.String())
	}

	// PUT a known model persists it.
	w = do(http.MethodPut, `{"model":"m2"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("PUT status=%d body=%q want 202", w.Code, w.Body.String())
	}
	raw, _ := os.ReadFile(configFile)
	if !strings.Contains(string(raw), "defaultModel: m2") {
		t.Errorf("config file missing defaultModel: m2:\n%s", raw)
	}

	// PUT an unknown model is rejected.
	w = do(http.MethodPut, `{"model":"nope"}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("PUT unknown status=%d want 404", w.Code)
	}

	// PUT without a model is rejected.
	w = do(http.MethodPut, `{}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("PUT empty status=%d want 400", w.Code)
	}

	// DELETE removes the key from the file.
	w = do(http.MethodDelete, "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("DELETE status=%d body=%q want 202", w.Code, w.Body.String())
	}
	raw, _ = os.ReadFile(configFile)
	if strings.Contains(string(raw), "defaultModel") {
		t.Errorf("config file still contains defaultModel:\n%s", raw)
	}
}

func TestServer_APIDefaultModel_GetUnset(t *testing.T) {
	s := newTestServerWithConfig(config.Config{}, newStubRouter(nil, ""), newStubRouter(nil, ""))

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/config/default-model", nil))

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"model":null`) {
		t.Errorf("status=%d body=%q want model null", w.Code, w.Body.String())
	}
}
