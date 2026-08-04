package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner records every command invocation and returns canned results
// keyed on the executable name, so tests can assert exactly which commands
// each backend constructs without ever touching the network or filesystem
// beyond what os.Stat needs (venv-existence checks still hit t.TempDir()).
type fakeRunner struct {
	calls [][]string
	err   error // returned for every call when set
}

func (f *fakeRunner) run(ctx context.Context, name string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.err != nil {
		return "", f.err
	}
	return "", nil
}

func withFakeRunner(t *testing.T, f *fakeRunner) {
	t.Helper()
	orig := runCommand
	runCommand = f.run
	t.Cleanup(func() { runCommand = orig })
}

func TestLlamaCpp_InstallAndUpgrade_ReturnErrUseSystemUpgrade(t *testing.T) {
	m := llamacppManager{}
	if _, err := m.Install(context.Background(), ""); !errors.Is(err, ErrUseSystemUpgrade) {
		t.Errorf("Install error = %v, want ErrUseSystemUpgrade", err)
	}
	if _, err := m.Upgrade(context.Background(), ""); !errors.Is(err, ErrUseSystemUpgrade) {
		t.Errorf("Upgrade error = %v, want ErrUseSystemUpgrade", err)
	}
}

func TestMLX_Install_ConstructsVenvAndPipCommands(t *testing.T) {
	if !isAppleSilicon() {
		t.Skip("mlx install path is Apple-Silicon-gated; this host would correctly reject it (covered by the platform-gate test)")
	}
	venvDir := filepath.Join(t.TempDir(), "mlx-venv")
	f := &fakeRunner{}
	withFakeRunner(t, f)

	m := mlxManager{}
	if _, err := m.Install(context.Background(), venvDir); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(f.calls) < 2 {
		t.Fatalf("expected at least venv-create + pip-install calls, got %v", f.calls)
	}
	if f.calls[0][0] != "python3" || f.calls[0][1] != "-m" || f.calls[0][2] != "venv" {
		t.Errorf("first call = %v, want a `python3 -m venv` invocation", f.calls[0])
	}
	pip := f.calls[1]
	if !strings.HasSuffix(pip[0], filepath.Join(venvDir, "bin", "python")) {
		t.Errorf("pip call interpreter = %q, want the venv's own python", pip[0])
	}
	if got := strings.Join(pip[1:], " "); got != "-m pip install -U mlx-lm" {
		t.Errorf("pip args = %q, want %q", got, "-m pip install -U mlx-lm")
	}
}

func TestMLX_Install_DefaultVenvDir_WhenUnspecified(t *testing.T) {
	if !isAppleSilicon() {
		t.Skip("Apple-Silicon-gated")
	}
	f := &fakeRunner{}
	withFakeRunner(t, f)

	m := mlxManager{}
	// Empty venvDir must resolve to ~/.venv/mlx, not error or panic. Whether
	// ensureVenv's own call is present depends on whether this host already
	// has that venv (this proposal's own text describes exactly that setup on
	// a real m3 host) — assert on the pip call, which always happens, rather
	// than assuming a fixed call count.
	if _, err := m.Install(context.Background(), ""); err != nil {
		t.Fatalf("Install with empty venvDir: %v", err)
	}
	if len(f.calls) == 0 {
		t.Fatal("expected at least the pip-install call")
	}
	want := defaultVenvDir(BackendMLX)
	last := f.calls[len(f.calls)-1]
	if !strings.Contains(last[0], want) {
		t.Errorf("pip interpreter = %q, want it to contain default venv %q", last[0], want)
	}
}

func TestVLLM_Install_ConstructsVenvAndPipCommands(t *testing.T) {
	if !hasNVIDIA() {
		t.Skip("vllm install path is CUDA-gated; this host would correctly reject it (covered by the platform-gate test)")
	}
	venvDir := filepath.Join(t.TempDir(), "vllm-venv")
	f := &fakeRunner{}
	withFakeRunner(t, f)

	m := vllmManager{}
	if _, err := m.Install(context.Background(), venvDir); err != nil {
		t.Fatalf("Install: %v", err)
	}
	pip := f.calls[1]
	if got := strings.Join(pip[1:], " "); got != "-m pip install -U vllm" {
		t.Errorf("pip args = %q, want %q", got, "-m pip install -U vllm")
	}
}

func TestVLLM_Install_RejectsWithoutNVIDIA(t *testing.T) {
	if hasNVIDIA() {
		t.Skip("this host has an NVIDIA GPU; nothing to assert for the rejection path here")
	}
	f := &fakeRunner{}
	withFakeRunner(t, f)

	m := vllmManager{}
	_, err := m.Install(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("Install on a non-CUDA host must fail")
	}
	if len(f.calls) != 0 {
		t.Errorf("platform gate must reject BEFORE running any command, got calls %v", f.calls)
	}
}

func TestMLX_Install_RejectsOnNonAppleSilicon(t *testing.T) {
	if isAppleSilicon() {
		t.Skip("this host IS Apple Silicon; nothing to assert for the rejection path here")
	}
	f := &fakeRunner{}
	withFakeRunner(t, f)

	m := mlxManager{}
	_, err := m.Install(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("Install on a non-Apple-Silicon host must fail")
	}
	if len(f.calls) != 0 {
		t.Errorf("platform gate must reject BEFORE running any command, got calls %v", f.calls)
	}
}

func TestMLX_Upgrade_WithoutPriorInstall_ErrorsBeforeRunningPip(t *testing.T) {
	if !isAppleSilicon() {
		t.Skip("Apple-Silicon-gated")
	}
	f := &fakeRunner{}
	withFakeRunner(t, f)

	m := mlxManager{}
	_, err := m.Upgrade(context.Background(), filepath.Join(t.TempDir(), "never-installed"))
	if err == nil {
		t.Fatal("Upgrade on a venv that was never installed must fail")
	}
	if len(f.calls) != 0 {
		t.Errorf("must error before attempting any pip command, got calls %v", f.calls)
	}
}

func TestEnsureVenv_Idempotent_SkipsWhenAlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	pythonPath := venvPython(dir)
	if err := os.MkdirAll(filepath.Dir(pythonPath), 0o755); err != nil {
		t.Fatalf("test setup: %v", err)
	}
	if err := os.WriteFile(pythonPath, nil, 0o755); err != nil {
		t.Fatalf("test setup: %v", err)
	}
	f := &fakeRunner{}
	withFakeRunner(t, f)

	if err := ensureVenv(context.Background(), dir); err != nil {
		t.Fatalf("ensureVenv: %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("ensureVenv must not re-create an existing venv, got calls %v", f.calls)
	}
}
