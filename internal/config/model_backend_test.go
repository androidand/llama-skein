package config

import (
	"strings"
	"testing"
)

func TestLoadConfig_BackendMLXInfersUseModelName(t *testing.T) {
	cfg, err := LoadConfigFromReader(strings.NewReader(`
models:
  mlx-model:
    backend: mlx
    cmd: /tmp/mlx_lm.server --host 127.0.0.1 --port ${PORT} --model mlx-community/Qwen3.5-35B-A3B-4bit
`))
	if err != nil {
		t.Fatalf("LoadConfigFromReader: %v", err)
	}

	if got := cfg.Models["mlx-model"].UseModelName; got != "mlx-community/Qwen3.5-35B-A3B-4bit" {
		t.Fatalf("UseModelName = %q, want mlx-community/Qwen3.5-35B-A3B-4bit", got)
	}
}

func TestLoadConfig_BackendVLLMInfersServedModelName(t *testing.T) {
	cfg, err := LoadConfigFromReader(strings.NewReader(`
models:
  coder:
    backend: vllm
    cmd: python -m vllm.entrypoints.openai.api_server --host 127.0.0.1 --port ${PORT} --model /models/Qwen3.6-35B-A3B --served-model-name qwen3.6-35b-a3b
`))
	if err != nil {
		t.Fatalf("LoadConfigFromReader: %v", err)
	}

	if got := cfg.Models["coder"].UseModelName; got != "qwen3.6-35b-a3b" {
		t.Fatalf("UseModelName = %q, want qwen3.6-35b-a3b", got)
	}
}

func TestLoadConfig_BackendMLXRequiresModelFlagOrUseModelName(t *testing.T) {
	_, err := LoadConfigFromReader(strings.NewReader(`
models:
  mlx-model:
    backend: mlx
    cmd: /tmp/mlx_lm.server --host 127.0.0.1 --port ${PORT}
`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "backend mlx requires useModelName or a --model flag in cmd") {
		t.Fatalf("unexpected error: %v", err)
	}
}
