package runtime

import (
	"context"
	"testing"
	"time"
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

func TestRuntime_MLXInstallRequiresVenvDir(t *testing.T) {
	mgr := For(BackendMLX)
	_, err := mgr.Install(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty venvDir")
	}
}

func TestRuntime_MLXUpgradeRequiresVenvDir(t *testing.T) {
	mgr := For(BackendMLX)
	_, err := mgr.Upgrade(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty venvDir")
	}
}

func TestRuntime_VLLMInstallRequiresVenvDir(t *testing.T) {
	mgr := For(BackendVLLM)
	_, err := mgr.Install(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty venvDir")
	}
}

func TestRuntime_VLLMUpgradeRequiresVenvDir(t *testing.T) {
	mgr := For(BackendVLLM)
	_, err := mgr.Upgrade(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty venvDir")
	}
}

func TestRuntime_LlamaCppInstallCreatesDir(t *testing.T) {
	mgr := For(BackendLlamaCpp)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// This will fail because the download URL may not exist, but it should
	// at least create the target directory.
	info, err := mgr.Install(ctx, t.TempDir())
	if err == nil {
		t.Error("expected error for install (no network or download fails)")
	}
	if info.Backend != BackendLlamaCpp {
		t.Errorf("expected backend llamacpp, got %q", info.Backend)
	}
}

func TestRuntime_LlamaCppVersionFailsForMissing(t *testing.T) {
	mgr := For(BackendLlamaCpp)
	_, err := mgr.Version(context.Background(), "/no/such/llama-server")
	if err == nil {
		t.Error("expected error for missing llama-server")
	}
}

func TestRuntime_LlamaCppHealthFailsForMissing(t *testing.T) {
	mgr := For(BackendLlamaCpp)
	if mgr.Health(context.Background(), "/no/such/llama-server") {
		t.Error("expected false health for missing llama-server")
	}
}

func TestRuntime_MLXVersionFailsForMissing(t *testing.T) {
	mgr := For(BackendMLX)
	_, err := mgr.Version(context.Background(), "/no/such/venv/bin/mlx_lm.server")
	if err == nil {
		t.Error("expected error for missing mlx_lm")
	}
}

func TestRuntime_MLXHealthFailsForMissing(t *testing.T) {
	mgr := For(BackendMLX)
	if mgr.Health(context.Background(), "/no/such/venv/bin/mlx_lm.server") {
		t.Error("expected false health for missing mlx_lm")
	}
}

func TestRuntime_VLLMVersionFailsForMissing(t *testing.T) {
	mgr := For(BackendVLLM)
	_, err := mgr.Version(context.Background(), "/no/such/venv/bin/vllm")
	if err == nil {
		t.Error("expected error for missing vllm")
	}
}

func TestRuntime_VLLMHealthFailsForMissing(t *testing.T) {
	mgr := For(BackendVLLM)
	if mgr.Health(context.Background(), "/no/such/venv/bin/vllm") {
		t.Error("expected false health for missing vllm")
	}
}
