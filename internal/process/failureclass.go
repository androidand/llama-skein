package process

import "strings"

// FailureClass names why a backend failed, precisely enough for a caller to
// decide whether a different placement could rescue it. It is deliberately
// distinct from FailureCategory (start vs crash — *when* it failed).
type FailureClass string

const (
	// ClassGPUOOM: the device allocator refused. A placement that moves more
	// weight off the card can rescue this.
	ClassGPUOOM FailureClass = "gpu-oom"
	// ClassHostOOM: system memory ran out, or the kernel/cgroup OOM killer
	// took the process. Moving MORE weight to the host makes this worse.
	ClassHostOOM FailureClass = "host-oom"
	// ClassUnsupportedArch: the engine does not implement this model's
	// architecture. No placement helps.
	ClassUnsupportedArch FailureClass = "unsupported-arch"
	// ClassMissingShard: a weight file (or one shard of a split GGUF) is
	// absent or unreadable.
	ClassMissingShard FailureClass = "missing-shard"
	// ClassInvalidFlag: the engine rejected a command-line argument.
	ClassInvalidFlag FailureClass = "invalid-flag"
	// ClassBackendError: the compute backend could not initialize (no
	// device, driver/runtime mismatch).
	ClassBackendError FailureClass = "backend-error"
	// ClassCrashOther: failed for a reason we did not recognize. NEVER
	// guessed into a memory class — an unclassified failure must not trigger
	// a placement retry.
	ClassCrashOther FailureClass = "crash-other"
)

// IsMemory reports whether a class is memory-related, i.e. whether a
// different placement could plausibly rescue the model. Only these classes
// are eligible for the adaptive retry ladder.
func (c FailureClass) IsMemory() bool {
	return c == ClassGPUOOM || c == ClassHostOOM
}

// ExitInfo is the process-level truth about how a backend died. Code is the
// exit status (-1 when the process was signaled); Err is the Go error text
// from cmd.Wait ("signal: killed" for SIGKILL), which keeps this portable
// without reaching into syscall.WaitStatus.
type ExitInfo struct {
	Code int
	Err  string
}

// deviceWords mark an allocation failure as belonging to the GPU rather than
// the host. Matched only inside a line that already looks like an
// out-of-memory failure.
var deviceWords = []string{
	"cuda", "hip", "rocm", "vram", "metal", "vulkan", "sycl", "device memory", "gpu",
}

// classRule maps engine output to a class. Order matters: the first match
// wins, so specific patterns precede general ones.
var classRules = []struct {
	class    FailureClass
	patterns []string
}{
	{ClassUnsupportedArch, []string{
		"unknown model architecture",
		"unsupported model architecture",
		"unknown architecture",
		"unsupported architecture",
		"unknown arch",
	}},
	{ClassMissingShard, []string{
		"no such file or directory",
		"failed to open gguf file",
		"failed to load model from file",
		"missing split",
		"invalid split file",
		"unable to load model",
	}},
	{ClassInvalidFlag, []string{
		"invalid argument",
		"unrecognized argument",
		"unknown argument",
		"error while handling argument",
		"invalid parameter",
	}},
	{ClassBackendError, []string{
		"no usable gpu",
		"no compatible gpu",
		"failed to initialize backend",
		"hiperrornodevice",
		"no cuda-capable device",
		"failed to initialize hip",
		"backend is in error state",
	}},
}

// oomPatterns mark an allocation failure. The device/host split is decided
// separately, from the surrounding line.
var oomPatterns = []string{
	"out of memory",
	"outofmemory",
	"failed to allocate",
	"cannot allocate memory",
	"std::bad_alloc",
	"buffer allocation failed",
	"insufficient memory",
	"oom",
}

// ClassifyFailure determines why a backend failed. Process-level truth (a
// kill signal) is consulted first, engine output second; anything
// unrecognized stays ClassCrashOther rather than being guessed into a memory
// class, because only memory classes trigger a placement retry.
//
// A SIGKILL is read as a host OOM: inside a cgroup that is overwhelmingly
// what SIGKILL means for an inference process (the kernel OOM killer). An
// operator's manual `kill -9` classifies the same way — the cost is one
// conservative retry, whereas missing a real cgroup kill risks repeating a
// load that takes the host down.
func ClassifyFailure(exit ExitInfo, output string) FailureClass {
	if exit.Code == 137 || strings.Contains(strings.ToLower(exit.Err), "signal: killed") {
		return ClassHostOOM
	}

	lower := strings.ToLower(output)
	if lower == "" {
		return ClassCrashOther
	}

	// A memory failure is the one class whose device/host split needs the
	// surrounding line, so it is resolved per-line before the flat rules.
	for _, line := range strings.Split(lower, "\n") {
		if !containsAny(line, oomPatterns) {
			continue
		}
		if containsAny(line, deviceWords) {
			return ClassGPUOOM
		}
		// Ambiguous "out of memory" with no device context resolves to
		// host-oom deliberately: the host ladder is the conservative one, and
		// mistaking a host OOM for a GPU OOM would move even MORE weight into
		// the memory that just ran out.
		return ClassHostOOM
	}

	for _, rule := range classRules {
		if containsAny(lower, rule.patterns) {
			return rule.class
		}
	}
	return ClassCrashOther
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
