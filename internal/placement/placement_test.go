package placement

import (
	"strings"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/fit"
)

// moeShape builds a synthetic MoE shape: totalGB of weights, expertGB of
// which are routed experts spread evenly over layers. No 100 GB fixtures —
// planning math only needs dimensions.
func moeShape(totalGB, expertGB int64, layers int) fit.ModelShape {
	perLayer := map[int]int64{}
	expertTotal := expertGB << 30
	for i := range layers {
		perLayer[i] = expertTotal / int64(layers)
	}
	return fit.ModelShape{
		LayerCount:      int64(layers),
		EmbeddingLength: 7168,
		HeadCount:       128, HeadCountKV: 8,
		KeyLength: 128, ValueLength: 128,
		WeightBytes: totalGB << 30,
		TrainedCtx:  131072,
		IsMoE:       true, ExpertBytesTotal: expertTotal, ExpertBytesPerLayer: perLayer,
	}
}

func denseShape(totalGB int64, layers int) fit.ModelShape {
	return fit.ModelShape{
		LayerCount:      int64(layers),
		EmbeddingLength: 8192,
		HeadCount:       64, HeadCountKV: 8,
		WeightBytes: totalGB << 30,
		TrainedCtx:  32768,
	}
}

// z4Inputs approximates the target host: 48 GB card, ~99 GB effective
// available of 112 GiB total (raised LXC limit).
func z4Inputs(shape fit.ModelShape) Inputs {
	return Inputs{
		Shape:           shape,
		ConfiguredCtx:   32768,
		VRAMBudgetMB:    48 * 1024,
		HostAvailableMB: 99 * 1024,
		HostTotalMB:     112 * 1024,
	}
}

func TestCompute_SmallModel_FullGPU(t *testing.T) {
	p := Compute(z4Inputs(moeShape(20, 16, 48)))
	if p.Mode != ModeGPU {
		t.Fatalf("mode = %s (%s), want gpu", p.Mode, p.Reason)
	}
	if p.Applies() {
		t.Fatal("full-GPU plan must not rewrite the command")
	}
	if p.PerfClass != PerfNativeGPU || !p.Confident {
		t.Fatalf("perf=%s confident=%v", p.PerfClass, p.Confident)
	}
}

// The acceptance scenario: ~91 GB MoE quant (UD-IQ2_M-like) on a 48 GB card
// with ~99 GB host available must plan hybrid with a pinned --n-cpu-moe.
func TestCompute_DeepSeekLike_Hybrid(t *testing.T) {
	p := Compute(z4Inputs(moeShape(91, 82, 61)))
	if p.Mode != ModeHybrid {
		t.Fatalf("mode = %s (%s), want hybrid", p.Mode, p.Reason)
	}
	if p.NCpuMoe <= 0 || p.NCpuMoe > 61 {
		t.Fatalf("n_cpu_moe = %d", p.NCpuMoe)
	}
	var hasNCpuMoe bool
	for _, op := range p.FlagOps {
		if op.Name == "--n-cpu-moe" {
			hasNCpuMoe = true
		}
		// Pinning --n-cpu-moe makes the engine abandon its own fitting
		// ("tensor_buft_overrides already set by user, abort"), so emitting
		// --fit-target alongside it would be a silently ignored flag that
		// makes the plan look more complete than it is. Verified on z4.
		if op.Name == "--fit-target" || op.Name == "--fit-ctx" {
			t.Fatalf("a pinned MoE plan must not rely on engine fitting: %+v", p.FlagOps)
		}
	}
	if !hasNCpuMoe {
		t.Fatalf("ops = %+v", p.FlagOps)
	}
	// The GPU estimate must respect the budget; the host share carries most
	// of the experts.
	if p.Estimate.GPUMB > 48*1024 {
		t.Fatalf("gpu estimate %d exceeds card", p.Estimate.GPUMB)
	}
	if p.Estimate.HostMB <= 0 {
		t.Fatal("hybrid plan must place weights on the host")
	}
	if p.PerfClass != PerfCPUBoundHybrid {
		t.Fatalf("perf = %s, want cpu-bound-hybrid (most weights on host)", p.PerfClass)
	}
}

// The minimal n found must actually be minimal: n-1 must not fit.
func TestCompute_MoE_MinimalN(t *testing.T) {
	in := z4Inputs(moeShape(60, 50, 40))
	p := Compute(in)
	if p.Mode != ModeHybrid {
		t.Fatalf("mode = %s (%s)", p.Mode, p.Reason)
	}
	if p.NCpuMoe > 1 {
		smaller := in
		// Re-scoring with one fewer offloaded layer must overflow the card.
		res := fit.AnalyzeShape(in.Shape, fit.Params{
			NCpuMoe:       p.NCpuMoe - 1,
			VRAMTotalMB:   in.VRAMBudgetMB - in.Policy.GpuReserveMB(in.VRAMBudgetMB),
			ConfiguredCtx: smaller.ConfiguredCtx, Unproven: true,
		})
		if res.FitLevel == "good" || res.FitLevel == "perfect" || res.FitLevel == "tight" {
			t.Fatalf("n=%d fits too — planner did not find the minimum", p.NCpuMoe-1)
		}
	}
}

// A model whose experts exceed safe host RAM even fully offloaded → refuse,
// confidently (the 507 path).
func TestCompute_ExceedsBoth_Refuse(t *testing.T) {
	in := z4Inputs(moeShape(160, 150, 61)) // UD-Q8_K_XL-like on 48+99
	p := Compute(in)
	if p.Mode != ModeRefuse || !p.Confident {
		t.Fatalf("mode = %s confident=%v (%s), want confident refuse", p.Mode, p.Confident, p.Reason)
	}
}

// Dense oversized model: delegate to the engine's --fit, host-gated.
func TestCompute_DenseOversized_DelegatedHybrid(t *testing.T) {
	p := Compute(z4Inputs(denseShape(70, 80)))
	if p.Mode != ModeHybrid {
		t.Fatalf("mode = %s (%s), want hybrid", p.Mode, p.Reason)
	}
	for _, op := range p.FlagOps {
		if op.Name == "--n-cpu-moe" {
			t.Fatal("dense plan must not carry MoE flags")
		}
	}
	if !strings.Contains(p.Reason, "--fit") {
		t.Fatalf("reason should say the split is delegated: %s", p.Reason)
	}
}

// Dense model too big even for VRAM+host → refuse.
func TestCompute_DenseWayTooBig_Refuse(t *testing.T) {
	p := Compute(z4Inputs(denseShape(200, 80)))
	if p.Mode != ModeRefuse {
		t.Fatalf("mode = %s (%s), want refuse", p.Mode, p.Reason)
	}
}

// Pinned placement flags → custom, untouched, regardless of size.
func TestCompute_PinnedFlags_Custom(t *testing.T) {
	in := z4Inputs(moeShape(91, 82, 61))
	in.PinnedPlacement = true
	p := Compute(in)
	if p.Mode != ModeCustom || p.Applies() {
		t.Fatalf("mode = %s ops=%v, want untouched custom", p.Mode, p.FlagOps)
	}
}

// placement.mode gpu → never rewrite.
func TestCompute_PolicyGPU_NoRewrite(t *testing.T) {
	in := z4Inputs(moeShape(91, 82, 61))
	in.Policy = config.PlacementConfig{Mode: config.PlacementModeGPU}
	p := Compute(in)
	if p.Mode != ModeGPU || p.Applies() {
		t.Fatalf("mode = %s ops=%v", p.Mode, p.FlagOps)
	}
}

// Unknown host budget: no hybrid guessing — fail open as unknown.
func TestCompute_UnknownHostBudget_FailsOpen(t *testing.T) {
	in := z4Inputs(moeShape(91, 82, 61))
	in.HostAvailableMB, in.HostTotalMB = 0, 0
	p := Compute(in)
	if p.Mode != ModeUnknown || p.Applies() || p.Confident {
		t.Fatalf("mode = %s ops=%v confident=%v", p.Mode, p.FlagOps, p.Confident)
	}
}

// Unknown VRAM budget: same.
func TestCompute_UnknownVRAM_FailsOpen(t *testing.T) {
	in := z4Inputs(moeShape(91, 82, 61))
	in.VRAMBudgetMB = 0
	p := Compute(in)
	if p.Mode != ModeUnknown || p.Applies() {
		t.Fatalf("mode = %s", p.Mode)
	}
}

// Host reserve is respected: available minus reserve is the real budget.
func TestCompute_HostReserveRespected(t *testing.T) {
	in := z4Inputs(moeShape(91, 82, 61))
	// 60 GB available on a 112 GiB host: reserve max(12G, 11.2G)=12G leaves
	// ~48 GB — the ~80+ GB of offloaded experts cannot fit.
	in.HostAvailableMB = 60 * 1024
	p := Compute(in)
	if p.Mode == ModeHybrid {
		t.Fatalf("hybrid planned into the host reserve: %+v", p.Estimate)
	}
}

// CPU-only fallback: tiny VRAM, model fits host comfortably.
func TestCompute_CPUOnlyFallback(t *testing.T) {
	in := Inputs{
		Shape:           denseShape(10, 40),
		ConfiguredCtx:   8192,
		VRAMBudgetMB:    2 * 1024, // 2 GB card: nothing fits
		HostAvailableMB: 64 * 1024,
		HostTotalMB:     64 * 1024,
	}
	p := Compute(in)
	// Dense spill path would also accept this; either hybrid (delegated) or
	// cpu is acceptable as long as it doesn't refuse. The pure-CPU assertion:
	if p.Mode == ModeRefuse || p.Mode == ModeUnknown {
		t.Fatalf("mode = %s (%s)", p.Mode, p.Reason)
	}
}

// KV-cache policy: the planner never emits cache-type flags (quantization is
// policy-gated and currently never automatic).
func TestCompute_NeverQuantizesKV(t *testing.T) {
	for _, shape := range []fit.ModelShape{moeShape(91, 82, 61), denseShape(70, 80)} {
		p := Compute(z4Inputs(shape))
		for _, op := range p.FlagOps {
			if strings.Contains(op.Name, "cache-type") {
				t.Fatalf("plan emits KV quantization: %+v", op)
			}
		}
	}
}
