package operation

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveArtifactURL_ComposesTheExpectedResolveURL(t *testing.T) {
	got, err := ResolveArtifactURL("org/repo-GGUF", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "model-Q4_K_M.gguf")
	if err != nil {
		t.Fatalf("ResolveArtifactURL: %v", err)
	}
	want := "https://huggingface.co/org/repo-GGUF/resolve/deadbeefdeadbeefdeadbeefdeadbeefdeadbeef/model-Q4_K_M.gguf"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveArtifactURL_AllowsNestedSubdirectories(t *testing.T) {
	// Mirrors Skein's TestNormalizeHuggingFaceDownloadURL_Blob fixture
	// (org/repo-GGUF, subdir/model.Q6_K.gguf) — the safe-composition path
	// still needs to handle a real nested artifact layout, not just a
	// flat file.
	got, err := ResolveArtifactURL("org/repo-GGUF", "deadbeef", "subdir/model.Q6_K.gguf")
	if err != nil {
		t.Fatalf("ResolveArtifactURL: %v", err)
	}
	want := "https://huggingface.co/org/repo-GGUF/resolve/deadbeef/subdir/model.Q6_K.gguf"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveArtifactURL_EncodesSpecialCharactersInThePath(t *testing.T) {
	got, err := ResolveArtifactURL("org/repo", "deadbeef", "model file (v2).gguf")
	if err != nil {
		t.Fatalf("ResolveArtifactURL: %v", err)
	}
	want := "https://huggingface.co/org/repo/resolve/deadbeef/model%20file%20%28v2%29.gguf"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveArtifactURL_RejectsPathTraversal(t *testing.T) {
	cases := []string{
		"../../../etc/passwd",
		"subdir/../../../etc/passwd",
		"subdir/../secret",
		"./model.gguf",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			_, err := ResolveArtifactURL("org/repo", "deadbeef", path)
			if !errors.Is(err, ErrUntrustedSource) {
				t.Fatalf("path %q: err = %v, want ErrUntrustedSource", path, err)
			}
		})
	}
}

func TestResolveArtifactURL_RejectsAbsolutePath(t *testing.T) {
	_, err := ResolveArtifactURL("org/repo", "deadbeef", "/etc/passwd")
	if !errors.Is(err, ErrUntrustedSource) {
		t.Fatalf("err = %v, want ErrUntrustedSource", err)
	}
}

func TestResolveArtifactURL_RejectsEmptyPathSegments(t *testing.T) {
	cases := []string{"", "subdir//model.gguf", "model.gguf/"}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			_, err := ResolveArtifactURL("org/repo", "deadbeef", path)
			if !errors.Is(err, ErrUntrustedSource) {
				t.Fatalf("path %q: err = %v, want ErrUntrustedSource", path, err)
			}
		})
	}
}

func TestResolveArtifactURL_RejectsMalformedRepository(t *testing.T) {
	cases := []string{
		"just-a-repo-no-owner",
		"org/repo/extra-segment",
		"https://huggingface.co/org/repo",
		"../escape/repo",
		"org//repo",
		"",
	}
	for _, repo := range cases {
		t.Run(repo, func(t *testing.T) {
			_, err := ResolveArtifactURL(repo, "deadbeef", "model.gguf")
			if !errors.Is(err, ErrUntrustedSource) {
				t.Fatalf("repository %q: err = %v, want ErrUntrustedSource", repo, err)
			}
		})
	}
}

func TestResolveArtifactURL_RejectsNonShaRevision(t *testing.T) {
	cases := []string{
		"main",      // a mutable branch name, exactly what decision 2 forbids
		"v1.0",      // a tag, same reason
		"dead",      // too short: below the 7-char short-SHA minimum
		"nothex123", // contains non-hex letters
		"",
	}
	for _, revision := range cases {
		t.Run(revision, func(t *testing.T) {
			_, err := ResolveArtifactURL("org/repo", revision, "model.gguf")
			if !errors.Is(err, ErrUntrustedSource) {
				t.Fatalf("revision %q: err = %v, want ErrUntrustedSource", revision, err)
			}
		})
	}
}

func TestResolveArtifactURL_AcceptsShortSha(t *testing.T) {
	// A 7-char hex short SHA is the minimum accepted length.
	got, err := ResolveArtifactURL("org/repo", "abc1234", "model.gguf")
	if err != nil {
		t.Fatalf("ResolveArtifactURL: %v", err)
	}
	want := "https://huggingface.co/org/repo/resolve/abc1234/model.gguf"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveArtifactDestination_ComposesTheExpectedPath(t *testing.T) {
	modelsDir := t.TempDir()
	got, err := ResolveArtifactDestination(modelsDir, "org/repo-GGUF", "model-Q4_K_M.gguf")
	if err != nil {
		t.Fatalf("ResolveArtifactDestination: %v", err)
	}
	want := filepath.Join(modelsDir, "org", "repo-GGUF", "model-Q4_K_M.gguf")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveArtifactDestination_AllowsNestedSubdirectories(t *testing.T) {
	modelsDir := t.TempDir()
	got, err := ResolveArtifactDestination(modelsDir, "org/repo-GGUF", "subdir/model.gguf")
	if err != nil {
		t.Fatalf("ResolveArtifactDestination: %v", err)
	}
	want := filepath.Join(modelsDir, "org", "repo-GGUF", "subdir", "model.gguf")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveArtifactDestination_RejectsPathTraversal(t *testing.T) {
	modelsDir := t.TempDir()
	_, err := ResolveArtifactDestination(modelsDir, "org/repo", "../../../etc/passwd")
	if !errors.Is(err, ErrUntrustedSource) {
		t.Fatalf("err = %v, want ErrUntrustedSource", err)
	}
}

func TestResolveArtifactDestination_RejectsMalformedRepository(t *testing.T) {
	modelsDir := t.TempDir()
	_, err := ResolveArtifactDestination(modelsDir, "../escape/repo", "model.gguf")
	if !errors.Is(err, ErrUntrustedSource) {
		t.Fatalf("err = %v, want ErrUntrustedSource", err)
	}
}

func TestResolveArtifactDestination_ContainmentIsAnExactDirectoryPrefix(t *testing.T) {
	// The destination must be genuinely inside modelsDir (per filepath.Rel,
	// the correct containment check), not just share a textual prefix with
	// a sibling directory like "models-archive" — the bug a naive
	// strings.HasPrefix(dest, modelsDir) check (no separator) would have.
	parent := t.TempDir()
	modelsDir := filepath.Join(parent, "models")

	got, err := ResolveArtifactDestination(modelsDir, "org/repo", "model.gguf")
	if err != nil {
		t.Fatalf("ResolveArtifactDestination: %v", err)
	}
	rel, err := filepath.Rel(modelsDir, got)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("destination %q is not contained within %q (rel=%q, err=%v)", got, modelsDir, rel, err)
	}
}
