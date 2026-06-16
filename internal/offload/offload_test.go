package offload

import (
	"reflect"
	"testing"
)

func intp(i int) *int       { return &i }
func boolp(b bool) *bool    { return &b }
func strp(s string) *string { return &s }

func TestOffload_ForBackendDefaults(t *testing.T) {
	cases := map[string]string{
		"":         BackendLlamaCpp,
		"llamacpp": BackendLlamaCpp,
		"vllm":     BackendVLLM,
		"mlx":      BackendMLX,
		"unknown":  BackendLlamaCpp,
	}
	for in, want := range cases {
		if got := For(in).Backend(); got != want {
			t.Errorf("For(%q).Backend() = %q, want %q", in, got, want)
		}
	}
}

func TestOffload_LlamaCppOps(t *testing.T) {
	ops, warnings := For(BackendLlamaCpp).Ops(Spec{
		NCpuMoe:        intp(22),
		CpuMoe:         boolp(true),
		OverrideTensor: strp(`\.ffn_.*_exps\.=CPU`),
	})
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	want := []FlagOp{
		{Name: "--n-cpu-moe", Value: "22"},
		{Name: "--cpu-moe", Boolean: true},
		{Name: "--override-tensor", Value: `\.ffn_.*_exps\.=CPU`},
	}
	if !reflect.DeepEqual(ops, want) {
		t.Errorf("ops = %+v, want %+v", ops, want)
	}
}

func TestOffload_LlamaCppDisableRemoves(t *testing.T) {
	ops, _ := For(BackendLlamaCpp).Ops(Spec{
		NCpuMoe:        intp(0),
		CpuMoe:         boolp(false),
		OverrideTensor: strp(""),
	})
	for _, op := range ops {
		if !op.Remove {
			t.Errorf("expected Remove op for %q, got %+v", op.Name, op)
		}
	}
	if len(ops) != 3 {
		t.Errorf("expected 3 remove ops, got %d: %+v", len(ops), ops)
	}
}

func TestOffload_LlamaCppWarnsOnVLLMKnob(t *testing.T) {
	_, warnings := For(BackendLlamaCpp).Ops(Spec{CpuOffloadGB: intp(10)})
	if len(warnings) == 0 {
		t.Fatal("expected a warning for cpu_offload_gb on llamacpp")
	}
}

func TestOffload_VLLMOps(t *testing.T) {
	ops, warnings := For(BackendVLLM).Ops(Spec{
		CpuOffloadGB: intp(10),
		NCpuMoe:      intp(8),
	})
	want := []FlagOp{{Name: "--cpu-offload-gb", Value: "10"}}
	if !reflect.DeepEqual(ops, want) {
		t.Errorf("ops = %+v, want %+v", ops, want)
	}
	if len(warnings) == 0 {
		t.Error("expected a warning for n_cpu_moe on vllm")
	}
}

func TestOffload_MLXIgnoresWithWarning(t *testing.T) {
	ops, warnings := For(BackendMLX).Ops(Spec{NCpuMoe: intp(4)})
	if len(ops) != 0 {
		t.Errorf("mlx should emit no ops, got %+v", ops)
	}
	if len(warnings) == 0 {
		t.Error("expected a warning for offload on mlx")
	}
	// Empty spec produces nothing at all.
	if ops, warnings := For(BackendMLX).Ops(Spec{}); len(ops) != 0 || len(warnings) != 0 {
		t.Errorf("empty mlx spec should be silent, got ops=%v warnings=%v", ops, warnings)
	}
}

func TestOffload_LlamaCppParse(t *testing.T) {
	args := []string{
		"llama-server", "--model", "/m.gguf",
		"--n-cpu-moe", "22", "--cpu-moe", "--override-tensor", "exps=CPU",
	}
	got := For(BackendLlamaCpp).Parse(args)
	if got.NCpuMoe == nil || *got.NCpuMoe != 22 {
		t.Errorf("NCpuMoe = %v, want 22", got.NCpuMoe)
	}
	if got.CpuMoe == nil || !*got.CpuMoe {
		t.Errorf("CpuMoe = %v, want true", got.CpuMoe)
	}
	if got.OverrideTensor == nil || *got.OverrideTensor != "exps=CPU" {
		t.Errorf("OverrideTensor = %v, want exps=CPU", got.OverrideTensor)
	}
}

func TestOffload_LlamaCppParseShortAndEquals(t *testing.T) {
	args := []string{"llama-server", "-ncmoe=16", "-cmoe"}
	got := For(BackendLlamaCpp).Parse(args)
	if got.NCpuMoe == nil || *got.NCpuMoe != 16 {
		t.Errorf("NCpuMoe = %v, want 16", got.NCpuMoe)
	}
	if got.CpuMoe == nil || !*got.CpuMoe {
		t.Errorf("CpuMoe = %v, want true", got.CpuMoe)
	}
}

func TestOffload_VLLMParse(t *testing.T) {
	got := For(BackendVLLM).Parse([]string{"vllm", "serve", "--cpu-offload-gb", "10"})
	if got.CpuOffloadGB == nil || *got.CpuOffloadGB != 10 {
		t.Errorf("CpuOffloadGB = %v, want 10", got.CpuOffloadGB)
	}
}

func TestOffload_SpecEmpty(t *testing.T) {
	if !(Spec{}).Empty() {
		t.Error("zero Spec should be Empty")
	}
	if (Spec{NCpuMoe: intp(0)}).Empty() {
		t.Error("Spec with a set pointer is not Empty")
	}
}
