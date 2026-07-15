package server

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestLemonadeGfxBucket(t *testing.T) {
	cases := []struct {
		gfx        string
		wantBucket string
		wantOK     bool
	}{
		{"gfx1100", "gfx110X", true}, // z4's Radeon Pro W7800
		{"gfx1101", "gfx110X", true},
		{"gfx1103", "gfx110X", true},
		{"gfx1201", "gfx120X", true},
		{"gfx1030", "gfx103X", true},
		{"gfx1151", "gfx1151", true}, // exact, not bucketed
		{"gfx90a", "gfx90a", true},
		{"gfx942", "", false}, // MI300 — not in lemonade-sdk's published buckets
		{"", "", false},
	}
	for _, c := range cases {
		bucket, ok := lemonadeGfxBucket(c.gfx)
		if bucket != c.wantBucket || ok != c.wantOK {
			t.Errorf("lemonadeGfxBucket(%q) = (%q,%v), want (%q,%v)", c.gfx, bucket, ok, c.wantBucket, c.wantOK)
		}
	}
}

func TestResolvePrebuiltSource(t *testing.T) {
	t.Run("RDNA3 gets the tailored lemonade-sdk build", func(t *testing.T) {
		src, err := resolvePrebuiltSource("gfx1100", "linux", "amd64", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if src.repo != "lemonade-sdk/llamacpp-rocm" {
			t.Errorf("repo = %q, want lemonade-sdk/llamacpp-rocm", src.repo)
		}
		if !src.tailored {
			t.Error("expected tailored=true for a lemonade-sdk gfx110X build")
		}
		if src.archiveFmt != "zip" {
			t.Errorf("archiveFmt = %q, want zip", src.archiveFmt)
		}
		if !src.matchAsset("llama-b1297-ubuntu-rocm-gfx110X-x64.zip") {
			t.Error("matchAsset should accept the real observed z4 asset name")
		}
		if src.matchAsset("llama-b1297-ubuntu-rocm-gfx120X-x64.zip") {
			t.Error("matchAsset must not accept a different gfx bucket's asset")
		}
	})

	t.Run("RDNA4 gets the tailored lemonade-sdk build (proxmox's arch)", func(t *testing.T) {
		src, err := resolvePrebuiltSource("gfx1201", "linux", "amd64", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if src.repo != "lemonade-sdk/llamacpp-rocm" || !src.tailored {
			t.Errorf("unexpected source for gfx1201: %+v", src)
		}
	})

	t.Run("AMD arch with no lemonade-sdk bucket REFUSES rather than falling back untailored", func(t *testing.T) {
		_, err := resolvePrebuiltSource("gfx942", "linux", "amd64", false)
		if err == nil {
			t.Fatal("expected a refusal error for an AMD arch lemonade-sdk doesn't publish, got nil")
		}
	})

	t.Run("macOS gets upstream's official arm64 build", func(t *testing.T) {
		src, err := resolvePrebuiltSource("", "darwin", "arm64", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if src.repo != "ggml-org/llama.cpp" {
			t.Errorf("repo = %q, want ggml-org/llama.cpp", src.repo)
		}
		if !src.matchAsset("llama-b10034-bin-macos-arm64.tar.gz") {
			t.Error("matchAsset should accept the real observed macos-arm64 asset name")
		}
		if src.matchAsset("llama-b10034-bin-macos-x64.tar.gz") {
			t.Error("matchAsset must not accept the Intel macOS asset for an arm64 host")
		}
	})

	t.Run("macOS Intel gets upstream's official x64 build", func(t *testing.T) {
		src, err := resolvePrebuiltSource("", "darwin", "amd64", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !src.matchAsset("llama-b10034-bin-macos-x64.tar.gz") {
			t.Error("matchAsset should accept the macos-x64 asset for an Intel Mac")
		}
	})

	t.Run("NVIDIA on Linux REFUSES — upstream publishes no Linux+CUDA release", func(t *testing.T) {
		_, err := resolvePrebuiltSource("", "linux", "amd64", true)
		if err == nil {
			t.Fatal("expected a refusal error for NVIDIA-on-Linux (no tailored or official prebuilt exists), got nil")
		}
	})

	t.Run("plain Linux host gets the CPU build", func(t *testing.T) {
		src, err := resolvePrebuiltSource("", "linux", "amd64", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if src.repo != "ggml-org/llama.cpp" || src.tailored {
			t.Errorf("unexpected source for plain Linux: %+v", src)
		}
		if !src.matchAsset("llama-b10032-bin-ubuntu-x64.tar.gz") {
			t.Error("matchAsset should accept the plain CPU asset")
		}
		// Must not accidentally match sibling variant assets from the same release.
		for _, other := range []string{
			"llama-b10032-bin-ubuntu-vulkan-x64.tar.gz",
			"llama-b10032-bin-ubuntu-rocm-7.2-x64.tar.gz",
			"llama-b10032-bin-ubuntu-sycl-fp16-x64.tar.gz",
			"llama-b10032-bin-ubuntu-arm64.tar.gz",
		} {
			if src.matchAsset(other) {
				t.Errorf("matchAsset incorrectly accepted sibling asset %q", other)
			}
		}
	})
}

func TestSelectReleaseAsset(t *testing.T) {
	assets := []githubAsset{
		{Name: "llama-b1297-win-rocm-gfx110X-x64.zip"},
		{Name: "llama-b1297-ubuntu-rocm-gfx110X-x64.zip"},
		{Name: "llama-b1297-ubuntu-rocm-gfx120X-x64.zip"},
	}
	src, err := resolvePrebuiltSource("gfx1100", "linux", "amd64", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, ok := selectReleaseAsset(assets, src)
	if !ok || got.Name != "llama-b1297-ubuntu-rocm-gfx110X-x64.zip" {
		t.Errorf("selectReleaseAsset = (%+v, %v), want the ubuntu gfx110X asset", got, ok)
	}
}

func TestDetectNvidia_NoPanicWhenAbsent(t *testing.T) {
	// Just exercise the real PATH lookup — this test environment has no
	// nvidia-smi, so the meaningful assertion is that it returns cleanly
	// rather than panicking, and consistently returns false here.
	if detectNvidia() {
		t.Skip("nvidia-smi unexpectedly present in this test environment")
	}
}

func TestStripTopLevelDir(t *testing.T) {
	cases := map[string]string{
		"llama-b1297-ubuntu-rocm-gfx110X-x64/llama-server":      "llama-server",
		"llama-b1297-ubuntu-rocm-gfx110X-x64/hipblaslt/gfx1100": "hipblaslt/gfx1100",
		"no-slash-at-all": "no-slash-at-all",
		"top/":            "",
	}
	for in, want := range cases {
		if got := stripTopLevelDir(in); got != want {
			t.Errorf("stripTopLevelDir(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestExtractArchive_PathEscapeRejected is a zip-slip regression: a malicious
// (or corrupt) archive entry with a ".." component must not be able to write
// outside destDir.
func TestExtractArchive_PathEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "evil.zip")
	buildTestZip(t, zipPath, map[string]string{
		"wrapper/../../escaped.txt": "should never be written outside destDir",
	})

	destDir := filepath.Join(dir, "extracted")
	if err := extractArchive(zipPath, destDir, "zip"); err == nil {
		t.Fatal("expected an error rejecting the path-escaping archive entry, got nil")
	}
	if _, err := os.Stat(filepath.Join(dir, "escaped.txt")); err == nil {
		t.Fatal("path-escaping entry was written outside destDir")
	}
}

func TestExtractArchive_ZipRoundTrip(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.zip")
	buildTestZip(t, zipPath, map[string]string{
		"llama-b1297-ubuntu-rocm-gfx110X-x64/llama-server":        "fake binary contents",
		"llama-b1297-ubuntu-rocm-gfx110X-x64/libggml.so":          "fake lib",
		"llama-b1297-ubuntu-rocm-gfx110X-x64/hipblaslt/library/x": "tuning data",
	})

	destDir := filepath.Join(dir, "extracted")
	if err := extractArchive(zipPath, destDir, "zip"); err != nil {
		t.Fatalf("extractArchive: %v", err)
	}

	// The wrapping top-level directory must be stripped — files land flat.
	for _, want := range []string{"llama-server", "libggml.so", "hipblaslt/library/x"} {
		p := filepath.Join(destDir, want)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected extracted file %s: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(destDir, "llama-b1297-ubuntu-rocm-gfx110X-x64")); err == nil {
		t.Error("the wrapping top-level directory should not itself exist in destDir")
	}
}

func TestExtractArchive_TarGzRoundTrip(t *testing.T) {
	dir := t.TempDir()
	tgzPath := filepath.Join(dir, "test.tar.gz")
	buildTestTarGz(t, tgzPath, map[string]string{
		"llama-b10032-bin-ubuntu-x64/llama-server": "fake binary contents",
		"llama-b10032-bin-ubuntu-x64/libggml.so":   "fake lib",
	})

	destDir := filepath.Join(dir, "extracted")
	if err := extractArchive(tgzPath, destDir, "tar.gz"); err != nil {
		t.Fatalf("extractArchive: %v", err)
	}
	for _, want := range []string{"llama-server", "libggml.so"} {
		if _, err := os.Stat(filepath.Join(destDir, want)); err != nil {
			t.Errorf("expected extracted file %s: %v", want, err)
		}
	}
}

func buildTestZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func buildTestTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}
