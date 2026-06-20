package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/pkg/apicontract"
)

func fitTestServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	return newTestServerWithConfig(cfg, newStubRouter(nil, ""), newStubRouter(nil, ""))
}

func getJSON(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestServer_Fit_UnknownModel404(t *testing.T) {
	s := fitTestServer(t, config.Config{Models: map[string]config.ModelConfig{}})
	if w := getJSON(t, s, "/api/fit/nope"); w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown model, got %d", w.Code)
	}
}

// MLX models report a clear "llamacpp only" reason rather than a wrong number.
func TestServer_Fit_MLXReportsUnsupported(t *testing.T) {
	cfg := config.Config{Models: map[string]config.ModelConfig{
		"mlx1": {Cmd: "mlx_lm.server --model mlx-community/x", Backend: config.BackendMLX},
	}}
	s := fitTestServer(t, cfg)
	w := getJSON(t, s, "/api/fit/mlx1")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	var mf apicontract.ModelFit
	json.Unmarshal(w.Body.Bytes(), &mf)
	if mf.FitLevel != apicontract.No || mf.Reason == nil {
		t.Errorf("expected fit=no with a reason for mlx, got level=%q reason=%v", mf.FitLevel, mf.Reason)
	}
}

// The report lists every configured model and is registered at /api/fit.
func TestServer_Fit_ReportListsModels(t *testing.T) {
	cfg := config.Config{Models: map[string]config.ModelConfig{
		"a": {Cmd: "mlx_lm.server --model x", Backend: config.BackendMLX},
		"b": {Cmd: "llama-server --model /missing.gguf"},
	}}
	s := fitTestServer(t, cfg)
	w := getJSON(t, s, "/api/fit")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var report apicontract.FitReport
	json.Unmarshal(w.Body.Bytes(), &report)
	if len(report.Models) != 2 {
		t.Errorf("expected 2 models in report, got %d", len(report.Models))
	}
}
