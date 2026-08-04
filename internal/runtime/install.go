// Phase 2+ of add-backend-runtime-management: install/upgrade for the
// backends that currently have neither (MLX, vLLM — both "installed and
// updated by hand" per the proposal). llama.cpp keeps its own dedicated,
// already-shipped endpoint (POST /api/system/upgrade) rather than being
// wrapped into this interface here — that upgrade path is deeply coupled to
// streaming the HTTP response (github release download, cmake build,
// ROCm/CUDA autodetect) and refactoring 900+ lines of working, production
// code behind a new interface for symmetry alone is deferred to its own
// change, not attempted blind in this one.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
)

// ErrUseSystemUpgrade is returned by llamacppManager's Install/Upgrade: this
// backend is managed through /api/system/upgrade, not this interface.
var ErrUseSystemUpgrade = errors.New("llama.cpp is managed via POST /api/system/upgrade, not this endpoint")

// Installer is implemented by backends that can be installed/upgraded
// through this interface. llamacppManager deliberately does not implement
// it (see ErrUseSystemUpgrade) — callers should type-assert or call
// InstallOrUpgrade, which returns ErrUseSystemUpgrade uniformly for it.
type Installer interface {
	// Install creates the backend's runtime environment from scratch.
	// venvDir is the target Python venv (mlx, vllm); empty means the
	// backend's default (~/.venv/<backend>).
	Install(ctx context.Context, venvDir string) (Info, error)
	// Upgrade updates an already-installed environment in place. Errors if
	// venvDir (or the default) was never installed.
	Upgrade(ctx context.Context, venvDir string) (Info, error)
}

// runCommand is a package-level indirection so tests can verify which
// commands each backend constructs without actually running pip/venv
// (network access, filesystem mutation, minutes of wall-clock — none of
// which belong in a unit test).
var runCommand = func(ctx context.Context, name string, args ...string) (string, error) {
	return runOutput(ctx, name, args...)
}

// defaultVenvDir returns "~/.venv/<backend>", the layout the proposal
// documents the existing hand-built MLX venv already uses.
func defaultVenvDir(backend string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".venv", backend)
}

// venvPython is the interpreter inside a venv at dir.
func venvPython(dir string) string { return filepath.Join(dir, "bin", "python") }

// ensureVenv creates a venv at dir if one doesn't already exist (idempotent —
// safe to call from both Install and Upgrade).
func ensureVenv(ctx context.Context, dir string) error {
	if _, err := os.Stat(venvPython(dir)); err == nil {
		return nil // already exists
	}
	if _, err := runCommand(ctx, "python3", "-m", "venv", dir); err != nil {
		return fmt.Errorf("creating venv at %s: %w", dir, err)
	}
	return nil
}

// pipInstall runs `<venv>/bin/python -m pip install -U <pkg>` — using
// `python -m pip` rather than a bare `pip` binary avoids depending on pip's
// own venv-relative shebang, which breaks if the venv is moved.
func pipInstall(ctx context.Context, venvDir, pkg string) error {
	if _, err := runCommand(ctx, venvPython(venvDir), "-m", "pip", "install", "-U", pkg); err != nil {
		return fmt.Errorf("pip install -U %s: %w", pkg, err)
	}
	return nil
}

// hasNVIDIA mirrors internal/server/apiupgrade.go's detectNvidia — same
// check (nvidia-smi on PATH), duplicated per this package's stated
// dependency-light design (see runtime.go's package doc).
func hasNVIDIA() bool {
	_, err := exec.LookPath("nvidia-smi")
	return err == nil
}

// isAppleSilicon reports whether this host can run MLX at all.
func isAppleSilicon() bool {
	return stdruntime.GOOS == "darwin" && stdruntime.GOARCH == "arm64"
}

// --- llama.cpp: not managed here, see ErrUseSystemUpgrade -----------------

func (llamacppManager) Install(ctx context.Context, venvDir string) (Info, error) {
	return Info{Backend: BackendLlamaCpp}, ErrUseSystemUpgrade
}

func (llamacppManager) Upgrade(ctx context.Context, venvDir string) (Info, error) {
	return Info{Backend: BackendLlamaCpp}, ErrUseSystemUpgrade
}

// --- MLX --------------------------------------------------------------------

func (m mlxManager) Install(ctx context.Context, venvDir string) (Info, error) {
	if !isAppleSilicon() {
		return Info{Backend: BackendMLX}, fmt.Errorf("mlx requires Apple Silicon (darwin/arm64), this host is %s/%s", stdruntime.GOOS, stdruntime.GOARCH)
	}
	if venvDir == "" {
		venvDir = defaultVenvDir(BackendMLX)
	}
	if err := ensureVenv(ctx, venvDir); err != nil {
		return Info{Backend: BackendMLX}, err
	}
	if err := pipInstall(ctx, venvDir, "mlx-lm"); err != nil {
		return Info{Backend: BackendMLX}, err
	}
	return m.Detect(ctx, filepath.Join(venvDir, "bin", "mlx_lm.server")), nil
}

func (m mlxManager) Upgrade(ctx context.Context, venvDir string) (Info, error) {
	if venvDir == "" {
		venvDir = defaultVenvDir(BackendMLX)
	}
	if _, err := os.Stat(venvPython(venvDir)); err != nil {
		return Info{Backend: BackendMLX}, fmt.Errorf("no mlx venv at %s — install first: %w", venvDir, err)
	}
	if err := pipInstall(ctx, venvDir, "mlx-lm"); err != nil {
		return Info{Backend: BackendMLX}, err
	}
	return m.Detect(ctx, filepath.Join(venvDir, "bin", "mlx_lm.server")), nil
}

// --- vLLM -------------------------------------------------------------------

func (m vllmManager) Install(ctx context.Context, venvDir string) (Info, error) {
	if !hasNVIDIA() {
		return Info{Backend: BackendVLLM}, errors.New("vllm requires an NVIDIA GPU with CUDA (nvidia-smi not found on PATH)")
	}
	if venvDir == "" {
		venvDir = defaultVenvDir(BackendVLLM)
	}
	if err := ensureVenv(ctx, venvDir); err != nil {
		return Info{Backend: BackendVLLM}, err
	}
	if err := pipInstall(ctx, venvDir, "vllm"); err != nil {
		return Info{Backend: BackendVLLM}, err
	}
	return m.Detect(ctx, filepath.Join(venvDir, "bin", "vllm")), nil
}

func (m vllmManager) Upgrade(ctx context.Context, venvDir string) (Info, error) {
	if venvDir == "" {
		venvDir = defaultVenvDir(BackendVLLM)
	}
	if _, err := os.Stat(venvPython(venvDir)); err != nil {
		return Info{Backend: BackendVLLM}, fmt.Errorf("no vllm venv at %s — install first: %w", venvDir, err)
	}
	if err := pipInstall(ctx, venvDir, "vllm"); err != nil {
		return Info{Backend: BackendVLLM}, err
	}
	return m.Detect(ctx, filepath.Join(venvDir, "bin", "vllm")), nil
}
