package gguf

import "testing"

// buildMoE returns a synthetic MoE GGUF with `layers` layers, each carrying one
// expert tensor and one non-expert tensor of `elems` elements. FileSize is set
// so that bytes-per-element is exactly 1, making the arithmetic easy to assert.
func buildMoE(layers, elems int) *GGUF {
	g := &GGUF{
		Architecture: "qwen3moe",
		ParamCount:   int64(layers * elems * 2),
		LayerCount:   int64(layers),
		ExpertCount:  8,
	}
	var total int64
	for l := range layers {
		name := "blk." + itoa(l) + "."
		g.Tensors = append(g.Tensors,
			TensorInfo{Name: name + "ffn_gate_exps.weight", Dims: []uint64{uint64(elems)}},
			TensorInfo{Name: name + "attn_q.weight", Dims: []uint64{uint64(elems)}},
		)
		total += int64(elems) * 2
	}
	g.FileSize = total // 1 byte per element
	return g
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestGGUF_ExpertWeightBytes(t *testing.T) {
	g := buildMoE(4, 1000) // total 8000 elems, FileSize 8000 -> 1 byte/elem
	total, perLayer, ok := g.ExpertWeightBytes()
	if !ok {
		t.Fatal("expected ok")
	}
	if total != 4000 {
		t.Errorf("total = %d, want 4000", total)
	}
	for l := range 4 {
		if perLayer[l] != 1000 {
			t.Errorf("perLayer[%d] = %d, want 1000", l, perLayer[l])
		}
	}
}

func TestGGUF_ExpertWeightBytes_NoTensorInfo(t *testing.T) {
	g := &GGUF{ExpertCount: 8, FileSize: 1000}
	if _, _, ok := g.ExpertWeightBytes(); ok {
		t.Error("expected ok=false without tensor info")
	}
}

func TestGGUF_RecommendCpuMoe_NotMoE(t *testing.T) {
	g := &GGUF{Architecture: "llama", LayerCount: 32, FileSize: 1000}
	plan := g.RecommendCpuMoe(1<<30, 8192)
	if plan.Applicable {
		t.Errorf("dense model should not be applicable: %+v", plan)
	}
}

func TestGGUF_RecommendCpuMoe_FitsFully(t *testing.T) {
	g := buildMoE(4, 1000) // weights 8000, kv 0 (EmbeddingLength unset), overhead 640
	plan := g.RecommendCpuMoe(10000, 8192)
	if !plan.Applicable || !plan.FitsFullyOnGPU || plan.NCpuMoe != 0 {
		t.Errorf("expected fits-fully with NCpuMoe 0, got %+v", plan)
	}
}

func TestGGUF_RecommendCpuMoe_PartialOffload(t *testing.T) {
	g := buildMoE(4, 1000) // required = 8640
	// deficit 640 -> first layer (frees 1000) is enough.
	if plan := g.RecommendCpuMoe(8000, 8192); plan.NCpuMoe != 1 || plan.FitsFullyOnGPU {
		t.Errorf("expected NCpuMoe 1, got %+v", plan)
	}
	// deficit 2640 -> layers 0,1,2 free 3000 >= 2640.
	if plan := g.RecommendCpuMoe(6000, 8192); plan.NCpuMoe != 3 {
		t.Errorf("expected NCpuMoe 3, got %+v", plan)
	}
}

func TestGGUF_RecommendCpuMoe_CannotFit(t *testing.T) {
	g := buildMoE(4, 1000) // all experts free only 4000
	plan := g.RecommendCpuMoe(1000, 8192)
	if plan.NCpuMoe != 4 || plan.FitsFullyOnGPU {
		t.Errorf("expected NCpuMoe = layerCount (4), got %+v", plan)
	}
}

func TestGGUF_RecommendCpuMoe_DimensionalFallback(t *testing.T) {
	// No tensor table; rely on dimensional estimate.
	g := &GGUF{
		Architecture:            "qwen3moe",
		ParamCount:              10_000_000,
		LayerCount:              8,
		EmbeddingLength:         512, // required by the dimensional estimate
		ExpertCount:             8,
		ExpertFeedForwardLength: 256,
		FeedForwardLength:       256,
		FileSize:                10_000_000,
	}
	// Ample VRAM: the model fits fully, but the estimate must still be applicable.
	plan := g.RecommendCpuMoe(1<<30, 4096)
	if !plan.Applicable {
		t.Fatalf("expected applicable with dimensional fallback, got %+v", plan)
	}
	if plan.ExpertBytesTotal <= 0 {
		t.Errorf("expected positive expert bytes estimate, got %+v", plan)
	}
}
