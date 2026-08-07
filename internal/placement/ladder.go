package placement

import (
	"fmt"
	"strconv"

	"github.com/androidand/llama-skein/internal/offload"
)

// Rung names a step on the retry ladder, in the order they are attempted.
type Rung string

const (
	// RungWidenReserve: hand the GPU more headroom / move more experts off
	// the card. The first and least damaging response to a GPU OOM.
	RungWidenReserve Rung = "widen-gpu-reserve"
	// RungShrinkBatch: smaller batch/ubatch — cuts transient compute
	// buffers without touching weights, context, or quality.
	RungShrinkBatch Rung = "shrink-batch"
	// RungShrinkContext: reduce --ctx-size, never below policy's minimum.
	RungShrinkContext Rung = "shrink-context"
	// RungFullCpuMoe: all experts to CPU — the most conservative placement
	// that still uses the GPU.
	RungFullCpuMoe Rung = "full-cpu-moe"
)

// ladderOrder is the escalation sequence for a GPU-side memory failure.
// Each rung gives up more than the last, so the first that loads wins.
var ladderOrder = []Rung{RungWidenReserve, RungShrinkBatch, RungShrinkContext, RungFullCpuMoe}

// retryBatchSize / retryUbatchSize are the reduced batch sizes rung 2 pins.
// llama.cpp's defaults (2048/512) size the transient compute buffers; these
// are the conservative values that still keep prompt processing usable.
const (
	retryBatchSize  = 512
	retryUbatchSize = 128
	// gpuReserveGrowthMB is added to the GPU reserve on each widen step.
	// Sized to clear typical ROCm/CUDA allocator fragmentation rather than
	// to be precise — the ladder is a search, not a calculation.
	gpuReserveGrowthMB = 2048
)

// Attempt records one rung of a retry ladder and how it ended, so the whole
// escalation is auditable after the fact rather than only in log lines.
type Attempt struct {
	Rung    Rung   `json:"rung"`
	Plan    Plan   `json:"plan"`
	Failure string `json:"failure,omitempty"`
}

// NextSaferPlan returns the next, more conservative plan after a
// memory-class failure. prevRungs is what has already been tried (in order),
// so the ladder never repeats a rung.
//
// ok=false means the ladder is exhausted — the caller must stop retrying and
// report the model failed, rather than restarting it forever.
//
// A host-OOM failure is NOT rescued by moving more weight to the host, so
// only the rungs that reduce host pressure (batch, context) apply to it;
// widening the GPU reserve or pushing all experts to CPU would make it
// worse. This asymmetry is the whole reason the failure class matters.
func NextSaferPlan(prev Plan, in Inputs, class string, prevRungs []Rung) (Plan, Rung, bool) {
	tried := map[Rung]bool{}
	for _, r := range prevRungs {
		tried[r] = true
	}
	hostSide := class == "host-oom"

	for _, rung := range ladderOrder {
		if tried[rung] {
			continue
		}
		if hostSide && (rung == RungWidenReserve || rung == RungFullCpuMoe) {
			continue // would move MORE weight into the memory that ran out
		}
		if plan, ok := applyRung(rung, prev, in, len(prevRungs)); ok {
			return plan, rung, true
		}
	}
	return Plan{}, "", false
}

// applyRung builds the plan for one rung from the previous plan. ok=false
// when the rung cannot help this model (no experts to move, context already
// at the policy floor), so the caller tries the next rung instead of
// retrying an identical command.
func applyRung(rung Rung, prev Plan, in Inputs, step int) (Plan, bool) {
	switch rung {
	case RungWidenReserve:
		// Re-plan with a bigger GPU reserve: the planner re-derives
		// --n-cpu-moe against the smaller budget, so this both widens the
		// margin and moves more experts off the card in one step.
		widened := in
		widened.VRAMBudgetMB = in.VRAMBudgetMB - gpuReserveGrowthMB*(step+1)
		if widened.VRAMBudgetMB <= 0 {
			return Plan{}, false
		}
		plan := Compute(widened)
		if plan.Mode != ModeHybrid && plan.Mode != ModeCPU {
			return Plan{}, false
		}
		plan.Reason = fmt.Sprintf("retry after GPU memory failure: %s (GPU budget reduced by %d MB)",
			plan.Reason, gpuReserveGrowthMB*(step+1))
		return plan, true

	case RungShrinkBatch:
		plan := prev
		plan.FlagOps = append(cloneOps(prev.FlagOps),
			offload.FlagOp{Name: "--batch-size", Value: strconv.Itoa(retryBatchSize)},
			offload.FlagOp{Name: "--ubatch-size", Value: strconv.Itoa(retryUbatchSize)},
		)
		plan.Reason = fmt.Sprintf("retry after memory failure: batch %d / ubatch %d to shrink transient compute buffers",
			retryBatchSize, retryUbatchSize)
		return plan, true

	case RungShrinkContext:
		minCtx := in.Policy.MinCtx()
		current := in.ConfiguredCtx
		if current <= 0 {
			current = int(in.Shape.TrainedCtx)
		}
		next := current / 2
		if next < minCtx {
			next = minCtx
		}
		if next >= current {
			return Plan{}, false // already at the floor; nothing to give
		}
		plan := prev
		plan.FlagOps = append(cloneOps(prev.FlagOps),
			offload.FlagOp{Name: "--ctx-size", Value: strconv.Itoa(next)},
		)
		plan.Reason = fmt.Sprintf("retry after memory failure: context reduced %d → %d (policy floor %d)",
			current, next, minCtx)
		return plan, true

	case RungFullCpuMoe:
		if !in.Shape.IsMoE || in.Shape.ExpertBytesTotal <= 0 {
			return Plan{}, false
		}
		plan := prev
		plan.NCpuMoe = int(in.Shape.LayerCount)
		plan.FlagOps = append(cloneOps(stripFlag(prev.FlagOps, "--n-cpu-moe")),
			offload.FlagOp{Name: "--cpu-moe", Boolean: true},
		)
		plan.Mode = ModeHybrid
		plan.PerfClass = PerfCPUBoundHybrid
		plan.Reason = "retry after GPU memory failure: all MoE experts moved to CPU"
		return plan, true
	}
	return Plan{}, false
}

func cloneOps(ops []offload.FlagOp) []offload.FlagOp {
	out := make([]offload.FlagOp, len(ops))
	copy(out, ops)
	return out
}

func stripFlag(ops []offload.FlagOp, name string) []offload.FlagOp {
	out := make([]offload.FlagOp, 0, len(ops))
	for _, op := range ops {
		if op.Name != name {
			out = append(out, op)
		}
	}
	return out
}
