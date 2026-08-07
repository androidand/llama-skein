package process

import (
	"strings"
	"testing"
)

// Synthetic log tails modelled on what each backend actually prints — no
// real models, no 100 GB fixtures.
func TestClassifyFailure(t *testing.T) {
	cases := []struct {
		name   string
		exit   ExitInfo
		output string
		want   FailureClass
	}{
		{
			name:   "rocm gpu oom",
			exit:   ExitInfo{Code: 1},
			output: "ggml_backend_hip_buffer_type_alloc_buffer: allocating 8192.00 MiB on device 0 failed: hipErrorOutOfMemory, out of memory",
			want:   ClassGPUOOM,
		},
		{
			name:   "cuda gpu oom",
			exit:   ExitInfo{Code: 1},
			output: "CUDA error: out of memory\n  current device: 0",
			want:   ClassGPUOOM,
		},
		{
			name:   "metal gpu oom",
			exit:   ExitInfo{Code: 1},
			output: "ggml_metal_graph_compute: command buffer 0 failed with CommandBufferCallbackErrorOutOfMemory",
			want:   ClassGPUOOM,
		},
		{
			name:   "vram allocation failure",
			exit:   ExitInfo{Code: 1},
			output: "llama_model_load: failed to allocate 41231 MiB of VRAM",
			want:   ClassGPUOOM,
		},
		{
			name:   "cgroup kill by exit code",
			exit:   ExitInfo{Code: 137},
			output: "",
			want:   ClassHostOOM,
		},
		{
			name:   "cgroup kill by signal text",
			exit:   ExitInfo{Code: -1, Err: "signal: killed"},
			output: "load_tensors: loading model tensors",
			want:   ClassHostOOM,
		},
		{
			name:   "host allocation failure",
			exit:   ExitInfo{Code: 1},
			output: "terminate called after throwing an instance of 'std::bad_alloc'",
			want:   ClassHostOOM,
		},
		{
			name: "ambiguous oom resolves to host (the conservative ladder)",
			exit: ExitInfo{Code: 1},
			// No device word: moving MORE weight to the host would make a
			// real host OOM worse, so this must not read as gpu-oom.
			output: "llama_model_load: error loading model: out of memory",
			want:   ClassHostOOM,
		},
		{
			name:   "unsupported architecture",
			exit:   ExitInfo{Code: 1},
			output: "llama_model_load: error loading model: unknown model architecture: 'deepseek4'",
			want:   ClassUnsupportedArch,
		},
		{
			name:   "missing gguf shard",
			exit:   ExitInfo{Code: 1},
			output: "llama_model_load: failed to open GGUF file '/models/x-00002-of-00003.gguf': No such file or directory",
			want:   ClassMissingShard,
		},
		{
			name:   "invalid flag",
			exit:   ExitInfo{Code: 1},
			output: "error while handling argument \"--n-cpu-moe\": invalid argument",
			want:   ClassInvalidFlag,
		},
		{
			name:   "backend unavailable",
			exit:   ExitInfo{Code: 1},
			output: "ggml_backend_hip_init: no usable GPU found",
			want:   ClassBackendError,
		},
		{
			name:   "unrecognized failure stays unclassified",
			exit:   ExitInfo{Code: 1},
			output: "Segmentation fault (core dumped)",
			want:   ClassCrashOther,
		},
		{
			name:   "no output at all stays unclassified",
			exit:   ExitInfo{Code: 1},
			output: "",
			want:   ClassCrashOther,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyFailure(c.exit, c.output); got != c.want {
				t.Fatalf("ClassifyFailure = %q, want %q", got, c.want)
			}
		})
	}
}

// Only memory classes may trigger a placement retry — a wrong answer here
// means retrying a model that can never load (arch, missing file, bad flag).
func TestFailureClass_IsMemory(t *testing.T) {
	memory := []FailureClass{ClassGPUOOM, ClassHostOOM}
	never := []FailureClass{ClassUnsupportedArch, ClassMissingShard, ClassInvalidFlag, ClassBackendError, ClassCrashOther}
	for _, c := range memory {
		if !c.IsMemory() {
			t.Errorf("%q must be retryable", c)
		}
	}
	for _, c := range never {
		if c.IsMemory() {
			t.Errorf("%q must NOT be retryable", c)
		}
	}
}

// A GPU OOM must not be misread as a host OOM just because the word "memory"
// appears near a host-ish phrase earlier in the log.
func TestClassifyFailure_DeviceLineWins(t *testing.T) {
	output := strings.Join([]string{
		"load_tensors: offloading 40 repeating layers to GPU",
		"ggml_backend_hip_buffer_type_alloc_buffer: allocating 4096.00 MiB on device 0 failed: hipErrorOutOfMemory",
	}, "\n")
	if got := ClassifyFailure(ExitInfo{Code: 1}, output); got != ClassGPUOOM {
		t.Fatalf("got %q, want gpu-oom", got)
	}
}
