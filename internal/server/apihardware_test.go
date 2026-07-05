package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/process"
)

// A loaded model on a host without perf telemetry (and whose GGUF the fit
// engine cannot parse) must still serve /api/hardware with kv_estimate_mb 0 —
// the fit fallback is best-effort and must never break the endpoint.
func TestServer_Hardware_KVEstimateFallbackDegradesGracefully(t *testing.T) {
	dir := t.TempDir()
	ggufPath := filepath.Join(dir, "tiny.gguf")
	// Not a valid GGUF: forces fitForModel's parse-error path, so the fallback
	// sees a ModelFit with a nil KvMbAtMaxSafeCtx.
	if err := os.WriteFile(ggufPath, []byte("not a gguf"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{Models: map[string]config.ModelConfig{
		"tiny": {Cmd: "llama-server --model " + ggufPath},
	}}
	local := newStubRouter([]string{"tiny"}, "")
	local.running = map[string]process.ProcessState{"tiny": process.StateReady}
	s := newTestServerWithConfig(cfg, local, newStubRouter(nil, ""))

	w := getJSON(t, s, "/api/hardware")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	var resp struct {
		LoadedModel *struct {
			ID           string `json:"id"`
			ModelMB      int64  `json:"model_mb"`
			KVEstimateMB int64  `json:"kv_estimate_mb"`
		} `json:"loaded_model"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%q", err, w.Body.String())
	}
	if resp.LoadedModel == nil {
		t.Fatalf("expected loaded_model in response, body=%q", w.Body.String())
	}
	if resp.LoadedModel.ID != "tiny" {
		t.Errorf("loaded_model.id = %q, want %q", resp.LoadedModel.ID, "tiny")
	}
	if resp.LoadedModel.KVEstimateMB != 0 {
		t.Errorf("kv_estimate_mb = %d, want 0 when neither VRAM delta nor fit is computable", resp.LoadedModel.KVEstimateMB)
	}
}
