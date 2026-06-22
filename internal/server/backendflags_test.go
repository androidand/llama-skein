package server

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
)

// llama.cpp-only flags (ctx_size, n_gpu_layers, cache_type_*) must not be
// written onto a non-llama.cpp backend's command — vllm serve rejects them just
// as mlx_lm.server does. Regression guard for the m3 "won't load" incident,
// generalized from MLX to every non-llama.cpp backend.
func TestServer_PatchLlamaCppFlags_GatedForVLLM(t *testing.T) {
	cmd := "vllm serve --model /m"
	yaml := "models:\n  v1:\n    backend: vllm\n    cmd: " + cmd + "\n"
	cfg := config.Config{Models: map[string]config.ModelConfig{
		"v1": {Cmd: cmd, Backend: config.BackendVLLM},
	}}
	s, cf := newOffloadTestServer(t, yaml, cfg)

	w := patchModel(t, s, "v1", `{"ctx_size":4096,"n_gpu_layers":99,"cache_type_k":"q8_0"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	raw, _ := os.ReadFile(cf)
	for _, f := range []string{"--ctx-size", "--n-gpu-layers", "--cache-type-k"} {
		if strings.Contains(string(raw), f) {
			t.Errorf("%s must not be written to a vllm command:\n%s", f, raw)
		}
	}
	var resp struct {
		Warnings []string `json:"warnings"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Warnings) == 0 {
		t.Errorf("expected warnings for gated llama.cpp flags, body=%q", w.Body.String())
	}
}

// The same flags must still apply normally on the default (llama.cpp) backend.
func TestServer_PatchLlamaCppFlags_AppliedForLlamaCpp(t *testing.T) {
	cmd := "llama-server --model /m.gguf"
	yaml := "models:\n  l1:\n    cmd: " + cmd + "\n"
	cfg := config.Config{Models: map[string]config.ModelConfig{"l1": {Cmd: cmd}}}
	s, cf := newOffloadTestServer(t, yaml, cfg)

	if w := patchModel(t, s, "l1", `{"ctx_size":8192}`); w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	if raw, _ := os.ReadFile(cf); !strings.Contains(string(raw), "--ctx-size 8192") {
		t.Errorf("llamacpp ctx_size must be applied:\n%s", raw)
	}
}
