package runtime

import (
	"context"
	"testing"
)

func TestRuntime_ForBackendDefaults(t *testing.T) {
	cases := map[string]string{
		"":         BackendLlamaCpp,
		"llamacpp": BackendLlamaCpp,
		"mlx":      BackendMLX,
		"vllm":     BackendVLLM,
		"unknown":  BackendLlamaCpp,
	}
	for in, want := range cases {
		if got := For(in).Backend(); got != want {
			t.Errorf("For(%q).Backend() = %q, want %q", in, got, want)
		}
	}
}

func TestRuntime_ParseLlamaCppVersion(t *testing.T) {
	cases := map[string]string{
		"version: 9140 (abcdef0)\nbuilt with Apple clang": "9140",
		"version: b9200":  "b9200",
		"no version line": "no version line",
	}
	for in, want := range cases {
		if got := parseLlamaCppVersion(in); got != want {
			t.Errorf("parseLlamaCppVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRuntime_PythonForEngine(t *testing.T) {
	cases := map[string]string{
		"/Users/a/.venv/mlx/bin/mlx_lm.server": "/Users/a/.venv/mlx/bin/python",
		"/opt/vllm/bin/vllm":                   "/opt/vllm/bin/python",
		"":                                     "python3",
		"mlx_lm.server":                        "python3", // bare name → PATH python3
	}
	for in, want := range cases {
		if got := pythonForEngine(in); got != want {
			t.Errorf("pythonForEngine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRuntime_DetectMissingEngine(t *testing.T) {
	// A non-existent venv must report not-installed with a reason, never panic.
	info := For(BackendMLX).Detect(context.Background(), "/no/such/venv/bin/mlx_lm.server")
	if info.Installed {
		t.Errorf("expected Installed=false for a missing engine, got %+v", info)
	}
	if info.Backend != BackendMLX || info.Detail == "" {
		t.Errorf("expected backend mlx + a reason, got %+v", info)
	}
}
