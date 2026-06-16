// Package offload translates backend-neutral CPU/MoE offload settings into the
// native command-line flags of each inference backend (llama.cpp, vLLM, MLX).
//
// The goal is that callers (the config API, opencode, skein) express intent in
// engine-agnostic terms — "offload the experts of N layers to CPU" — while each
// backend owns the mapping to its own flags. Adding a backend means adding a
// Translator, not touching every caller.
package offload

import (
	"slices"
	"strconv"
	"strings"
)

// Backend identifiers. These mirror internal/config's backend constants but are
// duplicated here to keep this package dependency-free and importable from both
// the config and server layers.
const (
	BackendLlamaCpp = "llamacpp"
	BackendMLX      = "mlx"
	BackendVLLM     = "vllm"
)

// Spec carries backend-neutral offload tuning knobs. A nil pointer means the
// field was not specified by the caller and must be left untouched. A zero/empty
// value means "disable" (remove the corresponding flag).
type Spec struct {
	NCpuMoe        *int    // experts of the first N layers -> CPU (0 disables)
	CpuMoe         *bool   // all MoE experts -> CPU (false disables)
	CpuOffloadGB   *int    // GiB of weights -> CPU, vLLM (0 disables)
	OverrideTensor *string // tensor placement regex, llama.cpp ("" disables)
}

// Empty reports whether no knob was specified.
func (s Spec) Empty() bool {
	return s.NCpuMoe == nil && s.CpuMoe == nil && s.CpuOffloadGB == nil && s.OverrideTensor == nil
}

// FlagOp is a single mutation to apply to a backend command line.
type FlagOp struct {
	Name    string // flag including leading dashes, e.g. "--n-cpu-moe"
	Value   string // value for value-flags; ignored when Boolean is true
	Boolean bool   // standalone flag taking no argument (e.g. --cpu-moe)
	Remove  bool   // remove the flag (and its value) from the command entirely
}

// Translator maps a backend-neutral Spec to native CLI flag operations and reads
// current offload settings back out of a command's argument list.
type Translator interface {
	// Backend returns the canonical backend identifier this translator targets.
	Backend() string
	// Ops returns the flag mutations for the given spec plus warnings for any
	// knob this backend does not support.
	Ops(Spec) (ops []FlagOp, warnings []string)
	// Parse reads the offload settings currently present in args (for read-back).
	Parse(args []string) Spec
}

// For returns the Translator for a backend identifier. An empty string defaults
// to llama.cpp, matching the config layer's treatment of the default backend.
func For(backend string) Translator {
	switch backend {
	case BackendMLX:
		return mlxTranslator{}
	case BackendVLLM:
		return vllmTranslator{}
	default:
		return llamacppTranslator{}
	}
}

// --- llama.cpp -------------------------------------------------------------

type llamacppTranslator struct{}

func (llamacppTranslator) Backend() string { return BackendLlamaCpp }

func (llamacppTranslator) Ops(s Spec) ([]FlagOp, []string) {
	var ops []FlagOp
	var warnings []string

	if s.NCpuMoe != nil {
		if *s.NCpuMoe > 0 {
			ops = append(ops, FlagOp{Name: "--n-cpu-moe", Value: strconv.Itoa(*s.NCpuMoe)})
		} else {
			ops = append(ops, FlagOp{Name: "--n-cpu-moe", Remove: true})
		}
	}
	if s.CpuMoe != nil {
		ops = append(ops, FlagOp{Name: "--cpu-moe", Boolean: true, Remove: !*s.CpuMoe})
	}
	if s.OverrideTensor != nil {
		if strings.TrimSpace(*s.OverrideTensor) != "" {
			ops = append(ops, FlagOp{Name: "--override-tensor", Value: *s.OverrideTensor})
		} else {
			ops = append(ops, FlagOp{Name: "--override-tensor", Remove: true})
		}
	}
	if s.CpuOffloadGB != nil {
		warnings = append(warnings,
			"cpu_offload_gb is not supported by the llamacpp backend; use n_cpu_moe or cpu_moe")
	}
	return ops, warnings
}

func (llamacppTranslator) Parse(args []string) Spec {
	var s Spec
	if v, ok := flagInt(args, "--n-cpu-moe", "-ncmoe"); ok {
		s.NCpuMoe = &v
	}
	if flagPresent(args, "--cpu-moe", "-cmoe") {
		t := true
		s.CpuMoe = &t
	}
	if v, ok := flagStr(args, "--override-tensor", "-ot"); ok {
		s.OverrideTensor = &v
	}
	return s
}

// --- vLLM ------------------------------------------------------------------

type vllmTranslator struct{}

func (vllmTranslator) Backend() string { return BackendVLLM }

func (vllmTranslator) Ops(s Spec) ([]FlagOp, []string) {
	var ops []FlagOp
	var warnings []string

	if s.CpuOffloadGB != nil {
		if *s.CpuOffloadGB > 0 {
			ops = append(ops, FlagOp{Name: "--cpu-offload-gb", Value: strconv.Itoa(*s.CpuOffloadGB)})
		} else {
			ops = append(ops, FlagOp{Name: "--cpu-offload-gb", Remove: true})
		}
	}
	if s.NCpuMoe != nil || s.CpuMoe != nil {
		warnings = append(warnings,
			"n_cpu_moe / cpu_moe are llama.cpp-only; use cpu_offload_gb for the vllm backend")
	}
	if s.OverrideTensor != nil {
		warnings = append(warnings, "override_tensor is not supported by the vllm backend")
	}
	return ops, warnings
}

func (vllmTranslator) Parse(args []string) Spec {
	var s Spec
	if v, ok := flagInt(args, "--cpu-offload-gb"); ok {
		s.CpuOffloadGB = &v
	}
	return s
}

// --- MLX -------------------------------------------------------------------

type mlxTranslator struct{}

func (mlxTranslator) Backend() string { return BackendMLX }

func (mlxTranslator) Ops(s Spec) ([]FlagOp, []string) {
	if s.Empty() {
		return nil, nil
	}
	return nil, []string{
		"mlx runs on Apple unified memory; CPU/MoE offload settings have no effect and are ignored",
	}
}

func (mlxTranslator) Parse([]string) Spec { return Spec{} }

// --- flag readers ----------------------------------------------------------

// flagPresent reports whether any of names appears as a standalone token.
func flagPresent(args []string, names ...string) bool {
	for _, n := range names {
		if slices.Contains(args, n) {
			return true
		}
	}
	return false
}

// flagStr returns the value of the first of names found as "--flag value" or
// "--flag=value".
func flagStr(args []string, names ...string) (string, bool) {
	for i, a := range args {
		for _, n := range names {
			if a == n && i+1 < len(args) {
				return args[i+1], true
			}
			if v, ok := strings.CutPrefix(a, n+"="); ok {
				return v, true
			}
		}
	}
	return "", false
}

func flagInt(args []string, names ...string) (int, bool) {
	v, ok := flagStr(args, names...)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, false
	}
	return n, true
}
