package server

import (
	"strings"
	"testing"
)

func TestBuildCmd_DoesNotInheritAnotherModelsFlags(t *testing.T) {
	// Regression: buildCmd previously copied the first existing model's full
	// command as a "structural template", silently propagating deepseek's
	// small-VRAM hybrid flags (--n-cpu-moe 25 --ctx-size 32768) onto every
	// auto-installed model — under-using larger cards. A fresh install must
	// get a minimal command and let llama.cpp's fit engine size the profile.
	s := newConfigWriteTestServer(t, "models:\n  deepseek:\n    cmd: llama-server --port ${PORT} --model /d.gguf --ctx-size 32768 --parallel 1 --n-cpu-moe 25\n    proxy: http://127.0.0.1:${PORT}\n")

	// No extra flags: must be the clean minimal command, no inherited flags.
	got := s.buildCmd("/models/model.gguf", "")
	if got != "llama-server --port ${PORT} --model /models/model.gguf" {
		t.Errorf("buildCmd(no flags) = %q, want minimal clean command", got)
	}
	if strings.Contains(got, "--n-cpu-moe") || strings.Contains(got, "--ctx-size") || strings.Contains(got, "deepseek") || strings.Contains(got, "/d.gguf") {
		t.Errorf("buildCmd(no flags) inherited another model's flags: %q", got)
	}

	// Explicit flags pass through after --model, and are not inherited either.
	gotFlags := s.buildCmd("/models/model.gguf", "--ctx-size 131072 --n-gpu-layers 99")
	want := "llama-server --port ${PORT} --model /models/model.gguf --ctx-size 131072 --n-gpu-layers 99"
	if gotFlags != want {
		t.Errorf("buildCmd(flags) = %q, want %q", gotFlags, want)
	}
}

// TestBuildCmd_UnsizableModelStaysMinimal is the fallback half of the
// recommended-ctx-size wiring: a GGUF that can't be read yet (still
// downloading, wrong path) must leave the command exactly as it was before
// that wiring existed — recommendedCtx's fallback has Max=0, and buildCmd
// must treat that as "nothing to add", not literally append "--ctx-size 0".
func TestBuildCmd_UnsizableModelStaysMinimal(t *testing.T) {
	s := newConfigWriteTestServer(t, "models: {}\n")
	got := s.buildCmd("/models/does-not-exist.gguf", "")
	want := "llama-server --port ${PORT} --model /models/does-not-exist.gguf"
	if got != want {
		t.Errorf("buildCmd(unsizable) = %q, want %q", got, want)
	}
}

func TestHasCtxSizeFlag(t *testing.T) {
	cases := []struct {
		name  string
		flags string
		want  bool
	}{
		{"empty", "", false},
		{"no ctx flag", "--n-gpu-layers 99 --parallel 1", false},
		{"long form", "--n-gpu-layers 99 --ctx-size 65536", true},
		{"short form", "-c 65536 --n-gpu-layers 99", true},
		{"equals form", "--ctx-size=65536", true},
		{"short equals form", "-c=65536", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasCtxSizeFlag(c.flags); got != c.want {
				t.Errorf("hasCtxSizeFlag(%q) = %v, want %v", c.flags, got, c.want)
			}
		})
	}
}

func TestNormalizeCmdPort(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "already macro-port is untouched",
			in:   "llama-server --port ${PORT} --model /m.gguf",
			want: "llama-server --port ${PORT} --model /m.gguf",
		},
		{
			name: "expanded literal port is normalized",
			in:   "llama-server --port 5800 --model /m.gguf",
			want: "llama-server --port ${PORT} --model /m.gguf",
		},
		{
			name: "no port flag is untouched",
			in:   "llama-server --model /m.gguf",
			want: "llama-server --model /m.gguf",
		},
		{
			name: "other ports are not touched",
			in:   "server --port 12345 --model /m.gguf --api-port 8080",
			want: "server --port ${PORT} --model /m.gguf --api-port 8080",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeCmdPort(tt.in); got != tt.want {
				t.Errorf("normalizeCmdPort(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
