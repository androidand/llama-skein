// Package runtime detects and (in later phases) installs/upgrades the inference
// engines that back each model: llama.cpp (llama-server), MLX (mlx_lm.server),
// and vLLM (vllm serve). It mirrors internal/offload — a backend-neutral
// interface plus per-backend implementations behind a registry — so callers
// never special-case an engine.
//
// Phase 1 (this file) covers detection: which engine a backend uses, whether it
// is installed, and its version. Install/upgrade and the control-API surface
// land in later phases (see openspec/changes/add-backend-runtime-management).
package runtime

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
)

// Backend identifiers, duplicated from internal/config to keep this package
// dependency-light (matching internal/offload's approach).
const (
	BackendLlamaCpp = "llamacpp"
	BackendMLX      = "mlx"
	BackendVLLM     = "vllm"
)

// Info is the detected state of a backend's inference engine on this host.
type Info struct {
	Backend   string `json:"backend"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	// Detail is a human-readable note: the resolved engine path when installed,
	// or why detection failed.
	Detail string `json:"detail,omitempty"`
}

// Manager detects and manages one backend's inference engine.
type Manager interface {
	Backend() string
	// Detect probes the engine. engineCmd is the command the model launches —
	// the first token of the model's cmd, e.g. an absolute mlx_lm.server path.
	// An empty engineCmd falls back to a PATH lookup of the well-known binary.
	Detect(ctx context.Context, engineCmd string) Info
}

// For returns the Manager for a backend identifier. An empty string defaults to
// llama.cpp, matching the config layer's treatment of the default backend.
func For(backend string) Manager {
	switch backend {
	case BackendMLX:
		return mlxManager{}
	case BackendVLLM:
		return vllmManager{}
	default:
		return llamacppManager{}
	}
}

// --- llama.cpp -------------------------------------------------------------

type llamacppManager struct{}

func (llamacppManager) Backend() string { return BackendLlamaCpp }

func (llamacppManager) Detect(ctx context.Context, engineCmd string) Info {
	bin := engineCmd
	if bin == "" {
		bin = "llama-server"
	}
	out, err := runOutput(ctx, bin, "--version")
	if err != nil && out == "" {
		return Info{Backend: BackendLlamaCpp, Detail: "llama-server not runnable: " + err.Error()}
	}
	return Info{Backend: BackendLlamaCpp, Installed: true, Version: parseLlamaCppVersion(out), Detail: bin}
}

// --- MLX -------------------------------------------------------------------

type mlxManager struct{}

func (mlxManager) Backend() string { return BackendMLX }

func (mlxManager) Detect(ctx context.Context, engineCmd string) Info {
	py := pythonForEngine(engineCmd)
	out, err := runOutput(ctx, py, "-c", "import mlx_lm;print(mlx_lm.__version__)")
	if err != nil {
		return Info{Backend: BackendMLX, Detail: "mlx_lm not importable via " + py + ": " + firstLine(out)}
	}
	return Info{Backend: BackendMLX, Installed: true, Version: firstLine(out), Detail: py}
}

// --- vLLM ------------------------------------------------------------------

type vllmManager struct{}

func (vllmManager) Backend() string { return BackendVLLM }

func (vllmManager) Detect(ctx context.Context, engineCmd string) Info {
	py := pythonForEngine(engineCmd)
	out, err := runOutput(ctx, py, "-c", "import vllm;print(vllm.__version__)")
	if err != nil {
		return Info{Backend: BackendVLLM, Detail: "vllm not importable via " + py + ": " + firstLine(out)}
	}
	return Info{Backend: BackendVLLM, Installed: true, Version: firstLine(out), Detail: py}
}

// --- helpers ---------------------------------------------------------------

func runOutput(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// pythonForEngine resolves the Python interpreter that runs a venv entrypoint:
// for "/x/.venv/mlx/bin/mlx_lm.server" it returns "/x/.venv/mlx/bin/python".
// For a bare name (or empty) it falls back to "python3" on PATH.
func pythonForEngine(engineCmd string) string {
	engineCmd = strings.TrimSpace(engineCmd)
	if dir := filepath.Dir(engineCmd); engineCmd != "" && strings.ContainsAny(engineCmd, "/\\") {
		return filepath.Join(dir, "python")
	}
	return "python3"
}

// parseLlamaCppVersion extracts the build number from `llama-server --version`
// output, which looks like "version: 9140 (abcdef)\nbuilt with ...".
func parseLlamaCppVersion(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "version:"); ok {
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				return fields[0]
			}
		}
	}
	return firstLine(out)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
