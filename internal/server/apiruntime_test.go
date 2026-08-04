package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/runtime"
	"github.com/androidand/llama-skein/pkg/apicontract"
)

func TestHandleListRuntimes_ReturnsAllThreeBackends(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/runtime", nil)
	rec := httptest.NewRecorder()
	s.handleListRuntimes(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var got []apicontract.RuntimeInfo
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d backends, want 3 (llamacpp, mlx, vllm)", len(got))
	}
	seen := map[string]bool{}
	for _, ri := range got {
		seen[string(ri.Backend)] = true
	}
	for _, want := range []string{"llamacpp", "mlx", "vllm"} {
		if !seen[want] {
			t.Errorf("missing backend %q in response %+v", want, got)
		}
	}
}

// The engine command detection must resolve to a CONFIGURED model's actual
// command, not just the well-known PATH binary — otherwise a non-standard
// install location is invisible to /api/runtime.
func TestRuntimeEngineCmd_UsesConfiguredModelCommand(t *testing.T) {
	s := &Server{cfg: config.Config{Models: map[string]config.ModelConfig{
		"m": {Backend: runtime.BackendMLX, Cmd: "/opt/custom/mlx_lm.server --model x"},
	}}}
	got := s.runtimeEngineCmd(runtime.BackendMLX)
	if got != "/opt/custom/mlx_lm.server" {
		t.Errorf("runtimeEngineCmd = %q, want the configured model's own binary path", got)
	}
}

func TestRuntimeEngineCmd_EmptyWhenNoBackendConfigured(t *testing.T) {
	s := &Server{cfg: config.Config{Models: map[string]config.ModelConfig{}}}
	if got := s.runtimeEngineCmd(runtime.BackendVLLM); got != "" {
		t.Errorf("runtimeEngineCmd with no configured model = %q, want empty (fall back to PATH default)", got)
	}
}

func TestHandleInstallRuntime_UnknownBackend_Returns400(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/notarealbackend/install", nil)
	rec := httptest.NewRecorder()
	s.handleInstallRuntime(rec, req, "notarealbackend")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// llama.cpp is the one backend without an Installer — the endpoint must
// point callers at the real upgrade path instead of a generic 500.
func TestHandleInstallRuntime_LlamaCpp_PointsAtSystemUpgrade(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/llamacpp/install", nil)
	rec := httptest.NewRecorder()
	s.handleInstallRuntime(rec, req, runtime.BackendLlamaCpp)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "system/upgrade") {
		t.Errorf("body = %q, want it to mention /api/system/upgrade", rec.Body.String())
	}
}

func TestHandleUpgradeRuntime_LlamaCpp_PointsAtSystemUpgrade(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/llamacpp/upgrade", nil)
	rec := httptest.NewRecorder()
	s.handleUpgradeRuntime(rec, req, runtime.BackendLlamaCpp)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleCheckRuntimeHealth_UnknownBackend_Returns400(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/bogus/health", nil)
	rec := httptest.NewRecorder()
	s.handleCheckRuntimeHealth(rec, req, "bogus")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleCheckRuntimeHealth_KnownBackend_ReturnsHealthPayload(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/llamacpp/health", nil)
	rec := httptest.NewRecorder()
	s.handleCheckRuntimeHealth(rec, req, runtime.BackendLlamaCpp)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var got apicontract.RuntimeHealth
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(got.Backend) != runtime.BackendLlamaCpp {
		t.Errorf("backend = %q, want %q", got.Backend, runtime.BackendLlamaCpp)
	}
}
