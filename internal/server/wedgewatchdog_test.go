package server

import (
	"testing"

	"github.com/androidand/llama-skein/internal/perf"
)

func TestStderrFatalMatch(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{
			name:   "empty history",
			input:  "",
			expect: "",
		},
		{
			name:   "no fatal pattern",
			input:  "normal output line\nanother normal line\n",
			expect: "",
		},
		{
			name:   "backend error state",
			input:  "some output\nbackend is in error state from a previous command buffer failure\nmore output\n",
			expect: "backend is in error state from a previous command buffer failure",
		},
		{
			name:   "OOM error",
			input:  "CommandBufferCallbackErrorOutOfMemory\n",
			expect: "CommandBufferCallbackErrorOutOfMemory",
		},
		{
			name:   "command buffer failed",
			input:  "command buffer failed with error code 123\n",
			expect: "command buffer failed with error",
		},
		{
			name:   "metal shader compile failure",
			input:  "failed to compile metal shader for kernel foo\n",
			expect: "failed to compile metal shader",
		},
		{
			name:   "case insensitive",
			input:  "BACKEND IS IN ERROR STATE FROM A PREVIOUS COMMAND BUFFER FAILURE\n",
			expect: "backend is in error state from a previous command buffer failure",
		},
		{
			name:   "mixed case",
			input:  "Backend Is In Error State From A Previous Command Buffer Failure\n",
			expect: "backend is in error state from a previous command buffer failure",
		},
		{
			name:   "pattern in middle of line",
			input:  "ERROR: command buffer failed with error on device 0\n",
			expect: "command buffer failed with error",
		},
		{
			name: "pattern outside last 20 lines is ignored",
			input: func() string {
				lines := make([]string, 25)
				for i := range lines {
					lines[i] = "normal line"
				}
				lines[0] = "backend is in error state from a previous command buffer failure"
				result := ""
				for i, line := range lines {
					if i > 0 {
						result += "\n"
					}
					result += line
				}
				return result
			}(),
			expect: "",
		},
		{
			name: "pattern in last 20 lines matches",
			input: func() string {
				lines := make([]string, 25)
				for i := range lines {
					lines[i] = "normal line"
				}
				lines[6] = "backend is in error state from a previous command buffer failure"
				result := ""
				for i, line := range lines {
					if i > 0 {
						result += "\n"
					}
					result += line
				}
				return result
			}(),
			expect: "backend is in error state from a previous command buffer failure",
		},
		{
			name:   "blank lines only",
			input:  "\n\n\n",
			expect: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stderrFatalMatch([]byte(tt.input))
			if got != tt.expect {
				t.Errorf("stderrFatalMatch() = %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestGpuStalled_MemActivityUnknown(t *testing.T) {
	g := perf.GpuStat{
		GpuUtilPct:       100,
		MemActivityPct:   0,
		MemActivityKnown: false,
	}
	if gpuStalled(g, 95, 20) {
		t.Error("gpuStalled should return false when MemActivityKnown is false")
	}
}

func TestGpuStalled_WedgeSignature(t *testing.T) {
	g := perf.GpuStat{
		GpuUtilPct:       100,
		MemActivityPct:   5,
		MemActivityKnown: true,
	}
	if !gpuStalled(g, 95, 20) {
		t.Error("gpuStalled should return true for wedge signature")
	}
}

func TestGpuStalled_NotStalled(t *testing.T) {
	g := perf.GpuStat{
		GpuUtilPct:       50,
		MemActivityPct:   80,
		MemActivityKnown: true,
	}
	if gpuStalled(g, 95, 20) {
		t.Error("gpuStalled should return false when GPU is not pinned busy")
	}
}
