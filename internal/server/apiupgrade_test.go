package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile creates path (and its parents) with the given contents.
func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestServer_CopyRuntimeDataDirs_InstallsTensileKernels guards the rocky crash:
// copySharedLibs matched only .so files, so every upgrade dropped the Tensile
// kernel trees the ROCm archive ships alongside them.
func TestServer_CopyRuntimeDataDirs_InstallsTensileKernels(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	writeFile(t, filepath.Join(src, "librocblas.so.5"), "elf")
	writeFile(t, filepath.Join(src, "rocblas", "library", "TensileLibrary_lazy_gfx1100.dat"), "kernels")
	writeFile(t, filepath.Join(src, "hipblaslt", "library", "gfx1100", "TensileLibrary_x.dat"), "kernels")

	if err := copySharedLibs(src, dst); err != nil {
		t.Fatalf("copySharedLibs: %v", err)
	}
	if err := copyRuntimeDataDirs(src, dst); err != nil {
		t.Fatalf("copyRuntimeDataDirs: %v", err)
	}

	for _, rel := range []string{
		filepath.Join("rocblas", "library", "TensileLibrary_lazy_gfx1100.dat"),
		filepath.Join("hipblaslt", "library", "gfx1100", "TensileLibrary_x.dat"),
	} {
		if _, err := os.Stat(filepath.Join(dst, rel)); err != nil {
			t.Errorf("kernel data %s not installed: %v", rel, err)
		}
	}

	if err := verifyRuntimeDataDirs(dst); err != nil {
		t.Errorf("verifyRuntimeDataDirs on a complete bundle: %v", err)
	}
}

// TestServer_VerifyRuntimeDataDirs_RejectsIncompleteBundle covers the state an
// upgrade left on rocky: .so present, kernels absent, `--version` still exits 0.
func TestServer_VerifyRuntimeDataDirs_RejectsIncompleteBundle(t *testing.T) {
	dst := t.TempDir()
	writeFile(t, filepath.Join(dst, "librocblas.so.5"), "elf")

	err := verifyRuntimeDataDirs(dst)
	if err == nil {
		t.Fatal("verifyRuntimeDataDirs returned nil for a bundle missing rocblas/library")
	}
	if !strings.Contains(err.Error(), "rocblas") {
		t.Errorf("error %q should name the offending library", err)
	}
}

// TestServer_VerifyRuntimeDataDirs_IgnoresAbsentLibrary: a CPU/CUDA bundle has
// no ROCm math library, so there is nothing to demand kernels for.
func TestServer_VerifyRuntimeDataDirs_IgnoresAbsentLibrary(t *testing.T) {
	dst := t.TempDir()
	writeFile(t, filepath.Join(dst, "libggml-cpu.so"), "elf")

	if err := verifyRuntimeDataDirs(dst); err != nil {
		t.Errorf("verifyRuntimeDataDirs on a non-ROCm bundle: %v", err)
	}
}

// TestServer_VerifyRuntimeDataDirs_RejectsEmptyKernelDir: a partial copy leaves
// the directory present but empty, which fails at runtime just the same.
func TestServer_VerifyRuntimeDataDirs_RejectsEmptyKernelDir(t *testing.T) {
	dst := t.TempDir()
	writeFile(t, filepath.Join(dst, "librocblas.so.5"), "elf")
	if err := os.MkdirAll(filepath.Join(dst, "rocblas", "library"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := verifyRuntimeDataDirs(dst); err == nil {
		t.Error("verifyRuntimeDataDirs returned nil for an empty rocblas/library")
	}
}
