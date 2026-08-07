package fit

import "testing"

// deepseekLikeShape approximates the z4 target scenario at small-integer
// scale: a MoE model whose weights vastly exceed VRAM but whose non-expert
// share fits — the case hybrid placement rescues. 100 GB weights, 90 GB of
// which are routed experts, on a 48 GB card.
func deepseekLikeShape() ModelShape {
	perLayer := map[int]int64{}
	layers := int64(60)
	expertTotal := int64(90) << 30
	for i := int64(0); i < layers; i++ {
		perLayer[int(i)] = expertTotal / layers
	}
	return ModelShape{
		LayerCount:          layers,
		EmbeddingLength:     7168,
		HeadCount:           128,
		HeadCountKV:         8, // stand-in; MLA is smaller still
		KeyLength:           128,
		ValueLength:         128,
		WeightBytes:         int64(100) << 30,
		TrainedCtx:          131072,
		IsMoE:               true,
		ExpertBytesTotal:    expertTotal,
		ExpertBytesPerLayer: perLayer,
	}
}

const testVRAM48G = 48 * 1024

// Full-GPU scoring of an oversized MoE model stays "no" (unproven): baseline.
func TestAnalyzeShape_OversizedMoE_NoOffload_IsNo(t *testing.T) {
	res := AnalyzeShape(deepseekLikeShape(), Params{
		VRAMTotalMB: testVRAM48G, ConfiguredCtx: 32768, Unproven: true,
	})
	if res.FitLevel != "no" {
		t.Fatalf("fit = %q, want no (100GB weights, 48GB card, no offload)", res.FitLevel)
	}
	if res.HostResidentMB != 0 || res.GPUResidentMB != res.ModelMB {
		t.Fatalf("split without offload: gpu=%d host=%d model=%d", res.GPUResidentMB, res.HostResidentMB, res.ModelMB)
	}
}

// --cpu-moe moves all expert bytes off the card: the same model now fits.
func TestAnalyzeShape_CpuMoeAll_Fits(t *testing.T) {
	res := AnalyzeShape(deepseekLikeShape(), Params{
		VRAMTotalMB: testVRAM48G, ConfiguredCtx: 32768, Unproven: true,
		CpuMoeAll: true, HostBudgetMB: 100 * 1024,
	})
	if res.FitLevel == "no" || res.FitLevel == "unknown" {
		t.Fatalf("fit = %q (%s), want a fitting level", res.FitLevel, res.Reason)
	}
	wantHost := int(int64(90) << 30 / (1 << 20))
	if res.HostResidentMB != wantHost {
		t.Fatalf("host resident = %d, want %d", res.HostResidentMB, wantHost)
	}
	if res.GPUResidentMB != res.ModelMB-wantHost {
		t.Fatalf("gpu resident = %d", res.GPUResidentMB)
	}
	// VRAM requirement must reflect only the GPU share.
	if res.VRAMRequiredMB > testVRAM48G {
		t.Fatalf("vram required = %d exceeds card", res.VRAMRequiredMB)
	}
}

// --n-cpu-moe N offloads the first N layers' experts, proportionally less
// than --cpu-moe.
func TestAnalyzeShape_NCpuMoe_PartialOffload(t *testing.T) {
	full := AnalyzeShape(deepseekLikeShape(), Params{
		VRAMTotalMB: testVRAM48G, ConfiguredCtx: 32768, Unproven: true,
		CpuMoeAll: true, HostBudgetMB: 100 * 1024,
	})
	half := AnalyzeShape(deepseekLikeShape(), Params{
		VRAMTotalMB: testVRAM48G, ConfiguredCtx: 32768, Unproven: true,
		NCpuMoe: 30, HostBudgetMB: 100 * 1024,
	})
	if half.HostResidentMB*2 != full.HostResidentMB {
		t.Fatalf("30/60 layers: host = %d, want half of %d", half.HostResidentMB, full.HostResidentMB)
	}
}

// Host budget is enforced: experts that don't fit safe host RAM are a "no",
// not a silent OOM plan. This is the check upstream --fit doesn't do.
func TestAnalyzeShape_HostBudgetExceeded_IsNo(t *testing.T) {
	res := AnalyzeShape(deepseekLikeShape(), Params{
		VRAMTotalMB: testVRAM48G, ConfiguredCtx: 32768, Unproven: true,
		CpuMoeAll: true, HostBudgetMB: 40 * 1024, // 90GB of experts vs 40GB budget
	})
	if res.FitLevel != "no" {
		t.Fatalf("fit = %q, want no on host budget breach", res.FitLevel)
	}
}

// Unknown host budget fails open: the offloaded share is not judged.
func TestAnalyzeShape_HostBudgetUnknown_FailsOpen(t *testing.T) {
	res := AnalyzeShape(deepseekLikeShape(), Params{
		VRAMTotalMB: testVRAM48G, ConfiguredCtx: 32768, Unproven: true,
		CpuMoeAll: true, HostBudgetMB: 0,
	})
	if res.FitLevel == "no" {
		t.Fatalf("fit = %q; unknown host budget must not refuse (%s)", res.FitLevel, res.Reason)
	}
}

// An explicit -ngl below the layer count moves the proportional non-expert
// share to the host.
func TestAnalyzeShape_NGpuLayers_DenseSplit(t *testing.T) {
	shape := ModelShape{
		LayerCount: 40, EmbeddingLength: 8192, HeadCount: 64, HeadCountKV: 8,
		WeightBytes: int64(40) << 30, TrainedCtx: 32768,
	}
	ngl := 10
	res := AnalyzeShape(shape, Params{
		VRAMTotalMB: 24 * 1024, ConfiguredCtx: 8192, Unproven: true,
		NGpuLayers: &ngl, HostBudgetMB: 64 * 1024,
	})
	wantHost := int(int64(40) << 30 / (1 << 20) * 30 / 40)
	if res.HostResidentMB != wantHost {
		t.Fatalf("host resident = %d, want %d (30/40 layers)", res.HostResidentMB, wantHost)
	}
}

// -ngl 0 is CPU-only: everything host-side.
func TestAnalyzeShape_NglZero_CPUOnly(t *testing.T) {
	shape := ModelShape{
		LayerCount: 40, EmbeddingLength: 8192, HeadCount: 64, HeadCountKV: 8,
		WeightBytes: int64(10) << 30, TrainedCtx: 32768,
	}
	zero := 0
	res := AnalyzeShape(shape, Params{
		VRAMTotalMB: 24 * 1024, ConfiguredCtx: 8192, Unproven: true,
		NGpuLayers: &zero, HostBudgetMB: 64 * 1024,
	})
	if res.HostResidentMB != res.ModelMB || res.GPUResidentMB != 0 {
		t.Fatalf("ngl 0: gpu=%d host=%d model=%d", res.GPUResidentMB, res.HostResidentMB, res.ModelMB)
	}
}

// cpu-moe + -ngl compose: experts go first, the dense remainder splits.
func TestCpuOffloadBytes_Compose(t *testing.T) {
	g := deepseekLikeShape()
	ngl := 30
	got := cpuOffloadBytes(g, Params{CpuMoeAll: true, NGpuLayers: &ngl})
	experts := g.ExpertBytesTotal
	dense := g.WeightBytes - experts
	want := experts + dense*30/60
	if got != want {
		t.Fatalf("compose = %d, want %d", got, want)
	}
}

// A deployed (configured, proven) hybrid model whose host budget is exceeded
// is still rescued to marginal — never discard a running model.
func TestAnalyzeShape_ConfiguredHostBreach_RescuedToMarginal(t *testing.T) {
	res := AnalyzeShape(deepseekLikeShape(), Params{
		VRAMTotalMB: testVRAM48G, ConfiguredCtx: 32768,
		CpuMoeAll: true, HostBudgetMB: 40 * 1024,
	})
	if res.FitLevel != "marginal" {
		t.Fatalf("fit = %q, want marginal for a configured model", res.FitLevel)
	}
}
