package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/androidand/llama-skein/internal/config"
)

func TestPatchCommandFlags(t *testing.T) {
	got, err := patchCommandFlags(
		"llama-server --port ${PORT} --model /models/a.gguf --ctx-size 8192 --n-gpu-layers=35",
		map[string]string{"--ctx-size": "32768", "--n-gpu-layers": "99", "--threads": "8"},
	)
	if err != nil {
		t.Fatalf("patchCommandFlags: %v", err)
	}
	for _, want := range []string{"--ctx-size 32768", "--n-gpu-layers=99", "--threads 8"} {
		if !strings.Contains(got, want) {
			t.Fatalf("patched cmd %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "8192") || strings.Contains(got, "35") {
		t.Fatalf("patched cmd kept old flag values: %q", got)
	}
}

func TestProxyManager_ApiConfigPatchModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "llama-swap.yaml")
	if err := os.WriteFile(configPath, []byte(`
modelsDir: /models
models:
  coder:
    cmd: llama-server --port ${PORT} --model /models/coder.gguf --ctx-size 8192 --n-gpu-layers 35
    name: Old name
    aliases:
      - old-alias
    ttl: 300
`), 0o644); err != nil {
		t.Fatal(err)
	}

	pm := &ProxyManager{
		configFile: configPath,
		config: config.Config{Models: map[string]config.ModelConfig{
			"coder": {Cmd: "llama-server --port ${PORT} --model /models/coder.gguf --ctx-size 8192 --n-gpu-layers 35"},
		}},
		ginEngine: gin.New(),
	}
	reloaded := make(chan struct{}, 1)
	pm.reloadFn = func() {
		reloaded <- struct{}{}
	}
	addApiHandlers(pm)

	body, _ := json.Marshal(map[string]any{
		"ctx_size":         32768,
		"n_gpu_layers":     99,
		"cache_type_k":     "q8_0",
		"cache_type_v":     "q8_0",
		"concurrencyLimit": 1,
		"ttl":              -1,
		"name":             "Coder",
		"flags": map[string]any{
			"threads": 8,
		},
	})
	req := httptest.NewRequest(http.MethodPatch, "/api/config/models/coder", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	pm.ginEngine.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("PATCH status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-reloaded:
	case <-time.After(time.Second):
		t.Fatal("reloadFn was not called")
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{
		"--ctx-size 32768",
		"--n-gpu-layers 99",
		"--cache-type-k q8_0",
		"--cache-type-v q8_0",
		"--threads 8",
		"concurrencyLimit: 1",
		"ttl: -1",
		"name: Coder",
		"modelsDir: /models",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("config missing %q:\n%s", want, out)
		}
	}
}

func TestProxyManager_ApiConfigGetModelIncludesOperationalKnobs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pm := &ProxyManager{
		config: config.Config{Models: map[string]config.ModelConfig{
			"coder": {
				Cmd:              "llama-server --port ${PORT} --model /models/coder.gguf --ctx-size 131072 --cache-type-k q8_0 --cache-type-v q8_0 --n-gpu-layers 99",
				ConcurrencyLimit: 1,
				UnloadAfter:      -1,
			},
		}},
		ginEngine: gin.New(),
	}
	addApiHandlers(pm)

	req := httptest.NewRequest(http.MethodGet, "/api/config/models/coder", nil)
	rec := httptest.NewRecorder()
	pm.ginEngine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", rec.Code, rec.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"ctx_size":         "131072",
		"cache_type_k":     "q8_0",
		"cache_type_v":     "q8_0",
		"n_gpu_layers":     "99",
		"concurrencyLimit": float64(1),
	} {
		if got[key] != want {
			t.Fatalf("%s = %#v, want %#v; body=%s", key, got[key], want, rec.Body.String())
		}
	}
}
