package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/offload"
)

func newOffloadTestServer(t *testing.T, yaml string, cfg config.Config) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cf := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cf, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestServerWithConfig(cfg, newStubRouter(nil, ""), newStubRouter(nil, ""))
	s.SetConfigFile(cf)
	return s, cf
}

func patchModel(t *testing.T, s *Server, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/config/models/"+id, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func TestServer_Offload_PatchLlamaCpp(t *testing.T) {
	cmd := "llama-server --model /m.gguf --ctx-size 4096"
	yaml := "models:\n  m1:\n    cmd: " + cmd + "\n"
	cfg := config.Config{Models: map[string]config.ModelConfig{"m1": {Cmd: cmd}}}
	s, cf := newOffloadTestServer(t, yaml, cfg)

	// Set n_cpu_moe -> flag added.
	if w := patchModel(t, s, "m1", `{"n_cpu_moe":22}`); w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	if raw, _ := os.ReadFile(cf); !strings.Contains(string(raw), "--n-cpu-moe 22") {
		t.Fatalf("expected --n-cpu-moe 22 in config:\n%s", raw)
	}

	// cpu_moe true -> boolean flag added.
	patchModel(t, s, "m1", `{"cpu_moe":true}`)
	if raw, _ := os.ReadFile(cf); !strings.Contains(string(raw), "--cpu-moe") {
		t.Fatalf("expected --cpu-moe in config:\n%s", raw)
	}

	// n_cpu_moe 0 -> flag removed.
	patchModel(t, s, "m1", `{"n_cpu_moe":0}`)
	raw, _ := os.ReadFile(cf)
	if strings.Contains(string(raw), "--n-cpu-moe") {
		t.Fatalf("expected --n-cpu-moe removed:\n%s", raw)
	}
	// cpu-moe (the boolean) must survive the n-cpu-moe removal.
	if !strings.Contains(string(raw), "--cpu-moe") {
		t.Fatalf("expected --cpu-moe retained:\n%s", raw)
	}
}

func TestServer_Offload_PatchDashAlias(t *testing.T) {
	cmd := "llama-server --model /m.gguf"
	yaml := "models:\n  m1:\n    cmd: " + cmd + "\n"
	cfg := config.Config{Models: map[string]config.ModelConfig{"m1": {Cmd: cmd}}}
	s, cf := newOffloadTestServer(t, yaml, cfg)

	patchModel(t, s, "m1", `{"n-cpu-moe":12}`)
	if raw, _ := os.ReadFile(cf); !strings.Contains(string(raw), "--n-cpu-moe 12") {
		t.Fatalf("expected dash alias to set --n-cpu-moe 12:\n%s", raw)
	}
}

func TestServer_Offload_PatchVLLMWarns(t *testing.T) {
	cmd := "vllm serve --model /m"
	yaml := "models:\n  v1:\n    backend: vllm\n    cmd: " + cmd + "\n"
	cfg := config.Config{Models: map[string]config.ModelConfig{
		"v1": {Cmd: cmd, Backend: config.BackendVLLM},
	}}
	s, cf := newOffloadTestServer(t, yaml, cfg)

	w := patchModel(t, s, "v1", `{"cpu_offload_gb":10,"n_cpu_moe":4}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	var resp struct {
		Warnings []string `json:"warnings"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Warnings) == 0 {
		t.Errorf("expected a warning for n_cpu_moe on vllm, body=%q", w.Body.String())
	}
	raw, _ := os.ReadFile(cf)
	if !strings.Contains(string(raw), "--cpu-offload-gb 10") {
		t.Errorf("expected --cpu-offload-gb 10:\n%s", raw)
	}
	if strings.Contains(string(raw), "--n-cpu-moe") {
		t.Errorf("n_cpu_moe must not leak to vllm command:\n%s", raw)
	}
}

func TestServer_Offload_ReadBackInModelList(t *testing.T) {
	cmd := "llama-server --model /m.gguf --n-cpu-moe 16 --cpu-moe"
	cfg := config.Config{Models: map[string]config.ModelConfig{"m1": {Cmd: cmd}}}
	s := newTestServerWithConfig(cfg, newStubRouter(nil, ""), newStubRouter(nil, ""))

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var list struct {
		Data []struct {
			Id      string `json:"id"`
			NCpuMoe *int   `json:"n_cpu_moe"`
			CpuMoe  *bool  `json:"cpu_moe"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range list.Data {
		if m.Id == "m1" {
			found = true
			if m.NCpuMoe == nil || *m.NCpuMoe != 16 {
				t.Errorf("n_cpu_moe = %v, want 16", m.NCpuMoe)
			}
			if m.CpuMoe == nil || !*m.CpuMoe {
				t.Errorf("cpu_moe = %v, want true", m.CpuMoe)
			}
		}
	}
	if !found {
		t.Fatalf("model m1 not in listing: %s", w.Body.String())
	}
}

func TestServer_Offload_RecommendationNotApplicable(t *testing.T) {
	cfg := config.Config{Models: map[string]config.ModelConfig{
		"mlx1": {Cmd: "mlx_lm.server --model x", Backend: config.BackendMLX},
		"l1":   {Cmd: "llama-server --port 8080"}, // no -m/--model
	}}
	s := newTestServerWithConfig(cfg, newStubRouter(nil, ""), newStubRouter(nil, ""))

	get := func(id string) string {
		req := httptest.NewRequest(http.MethodGet, "/api/models/offload/"+id, nil)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
		}
		return w.Body.String()
	}

	if body := get("mlx1"); !strings.Contains(body, `"applicable":false`) {
		t.Errorf("mlx should be not applicable: %s", body)
	}
	if body := get("l1"); !strings.Contains(body, `"applicable":false`) {
		t.Errorf("model without a file should be not applicable: %s", body)
	}
}

func TestServer_ApplyFlagOps(t *testing.T) {
	got, err := applyFlagOps("llama-server --model /m.gguf", []offload.FlagOp{
		{Name: "--n-cpu-moe", Value: "8"},
		{Name: "--cpu-moe", Boolean: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "--n-cpu-moe 8") || !strings.Contains(got, "--cpu-moe") {
		t.Fatalf("unexpected result: %q", got)
	}

	// Update in place rather than duplicate.
	got, _ = applyFlagOps("llama-server --n-cpu-moe 8", []offload.FlagOp{{Name: "--n-cpu-moe", Value: "20"}})
	if strings.Count(got, "--n-cpu-moe") != 1 || !strings.Contains(got, "--n-cpu-moe 20") {
		t.Fatalf("expected single updated flag, got %q", got)
	}

	// Remove value flag drops the value token too.
	got, _ = applyFlagOps("llama-server --n-cpu-moe 8 --ctx-size 4096", []offload.FlagOp{{Name: "--n-cpu-moe", Remove: true}})
	if strings.Contains(got, "--n-cpu-moe") || strings.Contains(got, " 8") {
		t.Fatalf("expected flag and value removed, got %q", got)
	}
	if !strings.Contains(got, "--ctx-size 4096") {
		t.Fatalf("unrelated flag should survive, got %q", got)
	}
}

// TestServer_PatchMLX_DropsCtxSizeNoOp verifies the churn fix: patching
// --ctx-size onto an mlx model is ignored (warning, cmd unchanged) and the
// no-op patch does not rewrite the file — so the config watcher won't reload
// and abort the running model.
func TestServer_PatchMLX_DropsCtxSizeNoOp(t *testing.T) {
	cmd := "/venv/bin/mlx_lm.server --model mlx-community/Qwen3.5-35B-A3B-4bit"
	yaml := "models:\n  mlx1:\n    backend: mlx\n    cmd: " + cmd + "\n"
	cfg := config.Config{Models: map[string]config.ModelConfig{
		"mlx1": {Cmd: cmd, Backend: config.BackendMLX},
	}}
	s, cf := newOffloadTestServer(t, yaml, cfg)
	before, _ := os.ReadFile(cf)

	w := patchModel(t, s, "mlx1", `{"ctx_size":262144}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	var resp struct {
		Warnings []string `json:"warnings"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Warnings) == 0 {
		t.Errorf("expected a warning for --ctx-size on mlx, body=%q", w.Body.String())
	}
	after, _ := os.ReadFile(cf)
	if string(after) != string(before) {
		t.Fatalf("mlx ctx_size patch must be a no-op; file changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if strings.Contains(string(after), "--ctx-size") {
		t.Fatalf("--ctx-size must never be written onto an mlx cmd:\n%s", after)
	}
}
