package server

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
)

// An explicitly configured enginePath is used verbatim, without consulting the
// process list. This is the whole point of the key: on a host running more than one
// engine build, discovery must not choose the destination.
func TestServer_CurrentServerPath_HonoursEnginePath(t *testing.T) {
	want := "/opt/llamacpp-instella/llama-server-instella"
	s := &Server{cfg: config.Config{EnginePath: want}}

	got, err := s.currentServerPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("currentServerPath() = %q, want %q", got, want)
	}
}

// Surrounding whitespace in the config value must not defeat the check, otherwise a
// stray space silently falls back to process discovery.
func TestServer_CurrentServerPath_TrimsEnginePath(t *testing.T) {
	s := &Server{cfg: config.Config{EnginePath: "  /opt/engine/llama-server  "}}

	got, err := s.currentServerPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/opt/engine/llama-server" {
		t.Errorf("currentServerPath() = %q, want the trimmed path", got)
	}
}

// With no enginePath and nothing installed at the fallback location, the error must
// name the config key so an operator knows how to resolve it rather than guessing.
func TestServer_CurrentServerPath_ErrorNamesEnginePath(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // ensure the ~/.local fallback cannot resolve
	s := &Server{cfg: config.Config{}}

	_, err := s.currentServerPath()
	if err == nil {
		t.Skip("a llama-server process is running on this host; discovery succeeded")
	}
	if !strings.Contains(err.Error(), "enginePath") {
		t.Errorf("error should name the enginePath key, got: %v", err)
	}
}

// The basename filter is what stops a differently-named engine build (the only
// available mitigation before enginePath existed) from being treated as the managed
// engine. `pgrep -a llama-server` matches it by pattern; the basename check must not.
func TestManagedServerBasename_RejectsSuffixedNames(t *testing.T) {
	cases := []struct {
		path string
		want bool // true = is the managed engine
	}{
		{"/usr/local/bin/llama-server", true},
		{"/opt/llamacpp-rocm-gfx110X/llama-server", true},
		{"/opt/llamacpp-instella/llama-server-instella", false},
		{"/opt/x/llama-server-cuda", false},
		{"/opt/x/my-llama-server", false},
	}
	for _, c := range cases {
		got := filepath.Base(c.path) == managedServerBasename
		if got != c.want {
			t.Errorf("basename check for %q = %v, want %v", c.path, got, c.want)
		}
	}
}
