// Package runtime detects, installs, upgrades, and monitors the inference
// engines that back each model: llama.cpp (llama-server), MLX (mlx_lm.server),
// and vLLM (vllm serve). It mirrors internal/offload — a backend-neutral
// interface plus per-backend implementations behind a registry — so callers
// never special-case an engine.
package runtime

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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
	// Error is set when an install/upgrade/health operation fails.
	Error string `json:"error,omitempty"`
}

// RuntimeHealth is the health status of a backend's inference engine.
type RuntimeHealth struct {
	Backend string `json:"backend"`
	Healthy bool   `json:"healthy"`
}

// Manager detects, installs, upgrades, and monitors one backend's inference engine.
type Manager interface {
	Backend() string
	// Detect probes the engine. engineCmd is the command the model launches —
	// the first token of the model's cmd, e.g. an absolute mlx_lm.server path.
	// An empty engineCmd falls back to a PATH lookup of the well-known binary.
	Detect(ctx context.Context, engineCmd string) Info
	// Install creates a venv at venvDir (or uses the system install for llamacpp)
	// and installs the engine package. Returns the resulting Info.
	Install(ctx context.Context, venvDir string) (Info, error)
	// Upgrade upgrades the engine in the existing venv at venvDir (or upgrades
	// the system binary for llamacpp). Returns the resulting Info.
	Upgrade(ctx context.Context, venvDir string) (Info, error)
	// Version returns the version string of the engine, or an error if not installed.
	Version(ctx context.Context, engineCmd string) (string, error)
	// Health returns true if the engine can serve a minimal request.
	Health(ctx context.Context, engineCmd string) bool
}

// Upgrader is implemented by backends that support a full upgrade flow with
// backup, rollback, smoke test, and restart. Currently only llama.cpp.
type Upgrader interface {
	UpgradeWithOptions(ctx context.Context, opts *UpgradeOptions) (Info, error)
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

func (m llamacppManager) Install(ctx context.Context, venvDir string) (Info, error) {
	// llamacpp installs to a system path, not a venv. venvDir is the target
	// directory for the binary (defaults to ~/.local/bin if empty).
	targetDir := venvDir
	if targetDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Info{Backend: BackendLlamaCpp, Error: "cannot determine home: " + err.Error()}, err
		}
		targetDir = filepath.Join(home, ".local", "bin")
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return Info{Backend: BackendLlamaCpp, Error: "create target dir: " + err.Error()}, err
	}
	binPath := filepath.Join(targetDir, "llama-server")
	// Use the prebuilt download path from the existing upgrade logic.
	info, err := installLlamaCppPrebuilt(ctx, targetDir)
	if err != nil {
		return Info{Backend: BackendLlamaCpp, Error: err.Error()}, err
	}
	info.Detail = binPath
	return info, nil
}

func (m llamacppManager) Version(ctx context.Context, engineCmd string) (string, error) {
	bin := engineCmd
	if bin == "" {
		bin = "llama-server"
	}
	out, err := runOutput(ctx, bin, "--version")
	if err != nil {
		return "", fmt.Errorf("llama-server --version: %w", err)
	}
	return parseLlamaCppVersion(out), nil
}

func (m llamacppManager) Health(ctx context.Context, engineCmd string) bool {
	bin := engineCmd
	if bin == "" {
		bin = "llama-server"
	}
	_, err := runOutput(ctx, bin, "--help")
	return err == nil
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

func (m mlxManager) Install(ctx context.Context, venvDir string) (Info, error) {
	if venvDir == "" {
		return Info{Backend: BackendMLX, Error: "venvDir is required for mlx install"}, fmt.Errorf("venvDir is required")
	}
	if err := createVenv(ctx, venvDir); err != nil {
		return Info{Backend: BackendMLX, Error: "create venv: " + err.Error()}, err
	}
	py := filepath.Join(venvDir, "bin", "python")
	if err := pipInstall(ctx, py, "mlx-lm"); err != nil {
		return Info{Backend: BackendMLX, Error: "pip install mlx-lm: " + err.Error()}, err
	}
	return m.Detect(ctx, filepath.Join(venvDir, "bin", "mlx_lm.server")), nil
}

func (m mlxManager) Upgrade(ctx context.Context, venvDir string) (Info, error) {
	if venvDir == "" {
		return Info{Backend: BackendMLX, Error: "venvDir is required for mlx upgrade"}, fmt.Errorf("venvDir is required")
	}
	py := filepath.Join(venvDir, "bin", "python")
	if _, err := os.Stat(py); os.IsNotExist(err) {
		return m.Install(ctx, venvDir)
	}
	if err := pipInstall(ctx, py, "-U", "mlx-lm"); err != nil {
		return Info{Backend: BackendMLX, Error: "pip install -U mlx-lm: " + err.Error()}, err
	}
	return m.Detect(ctx, filepath.Join(venvDir, "bin", "mlx_lm.server")), nil
}

func (m mlxManager) Version(ctx context.Context, engineCmd string) (string, error) {
	py := pythonForEngine(engineCmd)
	out, err := runOutput(ctx, py, "-c", "import mlx_lm;print(mlx_lm.__version__)")
	if err != nil {
		return "", fmt.Errorf("mlx_lm version: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func (m mlxManager) Health(ctx context.Context, engineCmd string) bool {
	py := pythonForEngine(engineCmd)
	_, err := runOutput(ctx, py, "-c", "import mlx_lm;print('ok')")
	return err == nil
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

func (m vllmManager) Install(ctx context.Context, venvDir string) (Info, error) {
	if venvDir == "" {
		return Info{Backend: BackendVLLM, Error: "venvDir is required for vllm install"}, fmt.Errorf("venvDir is required")
	}
	if err := createVenv(ctx, venvDir); err != nil {
		return Info{Backend: BackendVLLM, Error: "create venv: " + err.Error()}, err
	}
	py := filepath.Join(venvDir, "bin", "python")
	if err := pipInstall(ctx, py, "vllm"); err != nil {
		return Info{Backend: BackendVLLM, Error: "pip install vllm: " + err.Error()}, err
	}
	return m.Detect(ctx, filepath.Join(venvDir, "bin", "vllm")), nil
}

func (m vllmManager) Upgrade(ctx context.Context, venvDir string) (Info, error) {
	if venvDir == "" {
		return Info{Backend: BackendVLLM, Error: "venvDir is required for vllm upgrade"}, fmt.Errorf("venvDir is required")
	}
	py := filepath.Join(venvDir, "bin", "python")
	if _, err := os.Stat(py); os.IsNotExist(err) {
		return m.Install(ctx, venvDir)
	}
	if err := pipInstall(ctx, py, "-U", "vllm"); err != nil {
		return Info{Backend: BackendVLLM, Error: "pip install -U vllm: " + err.Error()}, err
	}
	return m.Detect(ctx, filepath.Join(venvDir, "bin", "vllm")), nil
}

func (m vllmManager) Version(ctx context.Context, engineCmd string) (string, error) {
	py := pythonForEngine(engineCmd)
	out, err := runOutput(ctx, py, "-c", "import vllm;print(vllm.__version__)")
	if err != nil {
		return "", fmt.Errorf("vllm version: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func (m vllmManager) Health(ctx context.Context, engineCmd string) bool {
	py := pythonForEngine(engineCmd)
	_, err := runOutput(ctx, py, "-c", "import vllm;print('ok')")
	return err == nil
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

func createVenv(ctx context.Context, venvDir string) error {
	py, err := exec.LookPath("python3")
	if err != nil {
		return fmt.Errorf("python3 not on PATH: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, "-m", "venv", venvDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create venv at %s: %w (output: %s)", venvDir, err, string(out))
	}
	return nil
}

func pipInstall(ctx context.Context, py string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmdArgs := append([]string{"-m", "pip", "install"}, args...)
	cmd := exec.CommandContext(ctx, py, cmdArgs...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pip install %v: %w (output: %s)", args, err, string(out))
	}
	return nil
}

func installLlamaCppPrebuilt(ctx context.Context, targetDir string) (Info, error) {
	arch := "x86_64"
	if runtime.GOARCH == "arm64" {
		arch = "aarch64"
	}
	osStr := runtime.GOOS
	if osStr == "darwin" {
		osStr = "macos"
	}
	url := fmt.Sprintf("https://github.com/ggml-org/llama.cpp/releases/latest/download/llama-server-%s-%s", osStr, arch)
	tmpFile, err := os.CreateTemp("", "llama-server-*.zip")
	if err != nil {
		return Info{}, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	client := &http.Client{Timeout: 5 * 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return Info{}, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Info{}, fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	f, err := os.Create(tmpPath + ".zip")
	if err != nil {
		return Info{}, fmt.Errorf("create zip file: %w", err)
	}
	_, err = io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		return Info{}, fmt.Errorf("write zip: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "unzip", "-o", tmpPath+".zip", "-d", targetDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Remove(tmpPath + ".zip")
		return Info{}, fmt.Errorf("unzip: %w (output: %s)", err, string(out))
	}
	os.Remove(tmpPath + ".zip")

	binPath := filepath.Join(targetDir, "llama-server")
	if err := os.Chmod(binPath, 0o755); err != nil {
		return Info{}, fmt.Errorf("chmod llama-server: %w", err)
	}

	return Info{Backend: BackendLlamaCpp, Installed: true, Detail: binPath}, nil
}
