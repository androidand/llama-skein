package placement

import (
	"strings"
	"testing"
)

func flagValue(p Plan, name string) (string, bool) {
	for _, op := range p.FlagOps {
		if op.Name == name {
			return op.Value, true
		}
	}
	return "", false
}

func hasFlag(p Plan, name string) bool {
	for _, op := range p.FlagOps {
		if op.Name == name {
			return true
		}
	}
	return false
}

// A GPU OOM walks the full ladder in order, and stops rather than looping.
func TestNextSaferPlan_GPUOOMLadderOrderAndExhaustion(t *testing.T) {
	in := z4Inputs(moeShape(91, 82, 61))
	prev := Compute(in)
	if prev.Mode != ModeHybrid {
		t.Fatalf("precondition: expected hybrid, got %s", prev.Mode)
	}

	var rungs []Rung
	for range len(ladderOrder) {
		plan, rung, ok := NextSaferPlan(prev, in, "gpu-oom", rungs)
		if !ok {
			break
		}
		rungs = append(rungs, rung)
		prev = plan
	}
	if len(rungs) == 0 {
		t.Fatal("a GPU OOM on a hybrid MoE model must offer at least one safer plan")
	}
	// Order must follow the declared escalation, never repeat.
	seen := map[Rung]bool{}
	lastIdx := -1
	for _, r := range rungs {
		if seen[r] {
			t.Fatalf("rung %q repeated", r)
		}
		seen[r] = true
		idx := -1
		for i, o := range ladderOrder {
			if o == r {
				idx = i
			}
		}
		if idx <= lastIdx {
			t.Fatalf("rung %q out of escalation order", r)
		}
		lastIdx = idx
	}
	// Exhaustion must terminate.
	if _, _, ok := NextSaferPlan(prev, in, "gpu-oom", rungs); ok {
		t.Fatal("ladder must be exhausted after every rung has been tried")
	}
}

// A host OOM must never be answered by moving MORE weight to the host.
func TestNextSaferPlan_HostOOMSkipsHostwardRungs(t *testing.T) {
	in := z4Inputs(moeShape(91, 82, 61))
	prev := Compute(in)

	var rungs []Rung
	for range len(ladderOrder) {
		_, rung, ok := NextSaferPlan(prev, in, "host-oom", rungs)
		if !ok {
			break
		}
		rungs = append(rungs, rung)
	}
	for _, r := range rungs {
		if r == RungWidenReserve || r == RungFullCpuMoe {
			t.Fatalf("host-oom must not offer %q — it moves more weight into the memory that ran out", r)
		}
	}
	if len(rungs) == 0 {
		t.Fatal("host-oom should still offer the batch/context rungs")
	}
}

// The batch rung pins reduced batch sizes without touching weights or ctx.
func TestNextSaferPlan_BatchRung(t *testing.T) {
	in := z4Inputs(moeShape(91, 82, 61))
	prev := Compute(in)
	plan, ok := applyRung(RungShrinkBatch, prev, in, 0)
	if !ok {
		t.Fatal("batch rung must always apply")
	}
	if v, _ := flagValue(plan, "--batch-size"); v != "512" {
		t.Fatalf("batch-size = %q", v)
	}
	if v, _ := flagValue(plan, "--ubatch-size"); v != "128" {
		t.Fatalf("ubatch-size = %q", v)
	}
	if hasFlag(plan, "--ctx-size") {
		t.Fatal("the batch rung must not change context")
	}
}

// Context reduction halves, but never below the policy floor, and reports
// that it stopped there rather than silently going lower.
func TestNextSaferPlan_ContextRungRespectsFloor(t *testing.T) {
	in := z4Inputs(moeShape(91, 82, 61))
	in.ConfiguredCtx = 32768
	prev := Compute(in)

	plan, ok := applyRung(RungShrinkContext, prev, in, 0)
	if !ok {
		t.Fatal("context rung should apply at 32768 with a 8192 floor")
	}
	if v, _ := flagValue(plan, "--ctx-size"); v != "16384" {
		t.Fatalf("ctx-size = %q, want 16384", v)
	}

	// Already at the floor: the rung must decline rather than emit a
	// no-op command that would just fail identically.
	atFloor := in
	atFloor.ConfiguredCtx = in.Policy.MinCtx()
	if _, ok := applyRung(RungShrinkContext, prev, atFloor, 0); ok {
		t.Fatal("context rung must decline once the policy floor is reached")
	}
}

// The last rung moves every expert to CPU and replaces any pinned
// --n-cpu-moe rather than emitting both flags.
func TestNextSaferPlan_FullCpuMoeReplacesPartial(t *testing.T) {
	in := z4Inputs(moeShape(91, 82, 61))
	prev := Compute(in)
	if !hasFlag(prev, "--n-cpu-moe") {
		t.Fatal("precondition: hybrid plan should pin --n-cpu-moe")
	}
	plan, ok := applyRung(RungFullCpuMoe, prev, in, 0)
	if !ok {
		t.Fatal("full-cpu-moe must apply to a MoE model")
	}
	if hasFlag(plan, "--n-cpu-moe") {
		t.Fatal("partial --n-cpu-moe must be removed when --cpu-moe is set")
	}
	if !hasFlag(plan, "--cpu-moe") {
		t.Fatal("--cpu-moe must be set")
	}
	if plan.PerfClass != PerfCPUBoundHybrid {
		t.Fatalf("perf class = %s", plan.PerfClass)
	}
}

// A dense model has no experts to move: that rung declines.
func TestNextSaferPlan_FullCpuMoeDeclinesForDense(t *testing.T) {
	in := z4Inputs(denseShape(70, 80))
	prev := Compute(in)
	if _, ok := applyRung(RungFullCpuMoe, prev, in, 0); ok {
		t.Fatal("a dense model has no MoE experts to move")
	}
}

// Widening the reserve re-plans against a smaller GPU budget, so it both
// widens the margin and pushes more experts off the card.
func TestNextSaferPlan_WidenReservePlansMoreOffload(t *testing.T) {
	in := z4Inputs(moeShape(91, 82, 61))
	prev := Compute(in)
	plan, ok := applyRung(RungWidenReserve, prev, in, 0)
	if !ok {
		t.Fatal("widen rung should apply")
	}
	if plan.NCpuMoe <= prev.NCpuMoe {
		t.Fatalf("n_cpu_moe did not increase: %d → %d", prev.NCpuMoe, plan.NCpuMoe)
	}
	if !strings.Contains(plan.Reason, "retry after GPU memory failure") {
		t.Fatalf("reason should record the retry: %s", plan.Reason)
	}
}
