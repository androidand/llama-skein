package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
)

func TestComputeModelSizeBytes(t *testing.T) {
	dir := t.TempDir()
	gguf := filepath.Join(dir, "m.gguf")
	if err := os.WriteFile(gguf, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		mc   config.ModelConfig
		want int64
	}{
		{"gguf file size", config.ModelConfig{Cmd: "llama-server --model " + gguf}, 4096},
		{"gguf missing path → 0", config.ModelConfig{Cmd: "llama-server --model /no/such/x.gguf"}, 0},
		{"gguf no --model → 0", config.ModelConfig{Cmd: "llama-server --port 9999"}, 0},
		{"mlx without useModelName → 0", config.ModelConfig{Backend: config.BackendMLX}, 0},
		{"vllm (unmodeled) → 0", config.ModelConfig{Backend: config.BackendVLLM}, 0},
	}
	for _, c := range cases {
		if got := computeModelSizeBytes(c.mc); got != c.want {
			t.Errorf("%s: computeModelSizeBytes = %d, want %d", c.name, got, c.want)
		}
	}
}
