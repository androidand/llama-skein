package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/logmon"
	"github.com/androidand/llama-skein/internal/process"
)

type captureLocalRouter struct {
	models   map[string]bool
	status   int
	body     string
	lastBody string
}

func newCaptureLocalRouter(models ...string) *captureLocalRouter {
	index := make(map[string]bool, len(models))
	for _, model := range models {
		index[model] = true
	}
	return &captureLocalRouter{
		models: index,
		status: http.StatusOK,
		body:   "OK",
	}
}

func (r *captureLocalRouter) Handles(model string) bool { return r.models[model] }
func (r *captureLocalRouter) Shutdown(time.Duration) error {
	return nil
}
func (r *captureLocalRouter) RunningModels() map[string]process.ProcessState { return nil }
func (r *captureLocalRouter) Unload(time.Duration, ...string)                {}
func (r *captureLocalRouter) ProcessLogger(string) (*logmon.Monitor, bool)   { return nil, false }
func (r *captureLocalRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.lastBody = string(body)
	w.WriteHeader(r.status)
	_, _ = w.Write([]byte(r.body))
}

func TestHandleAPILoadModel_RewritesUseModelName(t *testing.T) {
	local := newCaptureLocalRouter("mlx-qwen3-35b-a3b")
	s := newTestServer(local, newStubRouter(nil, ""))
	s.cfg = config.Config{
		Models: map[string]config.ModelConfig{
			"mlx-qwen3-35b-a3b": {
				UseModelName: "mlx-community/Qwen3.5-35B-A3B-4bit",
			},
		},
	}
	s.routes()

	req := httptest.NewRequest(http.MethodPost, "/api/models/load/mlx-qwen3-35b-a3b", nil).
		WithContext(context.Background())
	rr := httptest.NewRecorder()

	s.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	if !strings.Contains(local.lastBody, `"model":"mlx-community/Qwen3.5-35B-A3B-4bit"`) {
		t.Fatalf("warm-up body = %q", local.lastBody)
	}
}

func TestHandleAPILoadModel_PropagatesWarmupFailure(t *testing.T) {
	local := newCaptureLocalRouter("mlx-qwen3-35b-a3b")
	local.status = http.StatusNotFound
	local.body = `{"error":"missing upstream model"}`
	s := newTestServer(local, newStubRouter(nil, ""))
	s.cfg = config.Config{
		Models: map[string]config.ModelConfig{
			"mlx-qwen3-35b-a3b": {
				UseModelName: "mlx-community/Qwen3.5-35B-A3B-4bit",
			},
		},
	}
	s.routes()

	req := httptest.NewRequest(http.MethodPost, "/api/models/load/mlx-qwen3-35b-a3b", nil)
	rr := httptest.NewRecorder()

	s.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "missing upstream model") {
		t.Fatalf("body=%q", rr.Body.String())
	}
}
