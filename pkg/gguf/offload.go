package gguf

import (
	"regexp"
	"strconv"
	"strings"
)

// blkLayerRe extracts the layer index from a tensor name like "blk.12.ffn_...".
var blkLayerRe = regexp.MustCompile(`(?:^|\.)blk\.(\d+)\.`)

// isExpertTensor reports whether a tensor name is a MoE expert weight tensor.
// llama.cpp names them like "blk.N.ffn_gate_exps.weight", "...ffn_up_exps...",
// "...ffn_down_exps...". The "_exps" marker is the stable discriminator.
func isExpertTensor(name string) bool {
	return strings.Contains(name, "_exps")
}

func tensorElements(dims []uint64) int64 {
	if len(dims) == 0 {
		return 0
	}
	n := int64(1)
	for _, d := range dims {
		n *= int64(d)
	}
	return n
}

// HasTensorInfo reports whether the tensor table was parsed for this file.
func (g *GGUF) HasTensorInfo() bool { return len(g.Tensors) > 0 }

// ExpertWeightBytes returns the estimated total bytes of MoE expert tensors plus
// a per-layer breakdown keyed by layer index. Sizes come from exact per-tensor
// element counts scaled by the file's average bytes-per-element
// (FileSize / total_elements). This sidesteps hardcoding ggml quant block sizes
// while matching the FileSize-based fidelity used elsewhere in this package.
// ok is false when the tensor table or file size is unavailable.
func (g *GGUF) ExpertWeightBytes() (total int64, perLayer map[int]int64, ok bool) {
	if !g.HasTensorInfo() || g.FileSize <= 0 {
		return 0, nil, false
	}
	var totalElems, expertElems int64
	perLayerElems := map[int]int64{}
	for _, t := range g.Tensors {
		elems := tensorElements(t.Dims)
		totalElems += elems
		if !isExpertTensor(t.Name) {
			continue
		}
		expertElems += elems
		if m := blkLayerRe.FindStringSubmatch(t.Name); m != nil {
			if layer, err := strconv.Atoi(m[1]); err == nil {
				perLayerElems[layer] += elems
			}
		}
	}
	if totalElems <= 0 || expertElems <= 0 {
		return 0, nil, false
	}
	bytesPerElem := float64(g.FileSize) / float64(totalElems)
	total = int64(float64(expertElems) * bytesPerElem)
	perLayer = make(map[int]int64, len(perLayerElems))
	for layer, e := range perLayerElems {
		perLayer[layer] = int64(float64(e) * bytesPerElem)
	}
	return total, perLayer, true
}

// estimateExpertBytesFromDims approximates total expert weight bytes from
// architecture dimensions when the tensor table is unavailable. Expert FFN
// params per layer ~= expert_count * 3 (gate, up, down) * embedding * expert_ff.
// Returns 0 when the required dimensions are missing.
func (g *GGUF) estimateExpertBytesFromDims() int64 {
	if !g.IsMoE() || g.LayerCount <= 0 || g.EmbeddingLength <= 0 {
		return 0
	}
	expertFF := g.ExpertFeedForwardLength
	if expertFF <= 0 {
		expertFF = g.FeedForwardLength
	}
	if expertFF <= 0 {
		return 0
	}
	expertParams := g.LayerCount * g.ExpertCount * 3 * g.EmbeddingLength * expertFF
	if expertParams <= 0 {
		return 0
	}
	weights := g.WeightBytes()
	if g.ParamCount > 0 && weights > 0 {
		// Scale by the file's average bytes-per-param, clamping the expert
		// fraction so a noisy estimate can never exceed the whole model.
		ratio := float64(expertParams) / float64(g.ParamCount)
		if ratio > 0.98 {
			ratio = 0.98
		}
		return int64(float64(weights) * ratio)
	}
	// No param count: fall back to bits-per-weight.
	return (expertParams*int64(g.bitsPerWeight()) + 7) / 8
}

// OffloadPlan is the result of a CPU/MoE offload recommendation.
type OffloadPlan struct {
	Applicable       bool
	NCpuMoe          int   // recommended --n-cpu-moe (leading layers to offload)
	FitsFullyOnGPU   bool  // true when no offload is needed
	ExpertBytesTotal int64 // estimated total expert tensor bytes
	Reason           string
}

// RecommendCpuMoe computes a recommended --n-cpu-moe value: the fewest leading
// layers whose MoE experts must move to CPU so that the remaining weights, the
// KV cache for ctxLen, and overhead fit within freeBytes of VRAM.
//
// It is MoE-scoped: non-MoE models return Applicable=false. When the exact
// tensor table is available it is used; otherwise a dimensional estimate is.
func (g *GGUF) RecommendCpuMoe(freeBytes, ctxLen int64) OffloadPlan {
	if !g.IsMoE() {
		return OffloadPlan{Reason: "model is not a mixture-of-experts model; CPU/MoE offload does not apply"}
	}
	if freeBytes <= 0 {
		return OffloadPlan{Reason: "free VRAM is unknown; cannot compute a recommendation"}
	}

	expertTotal, perLayer, exact := g.ExpertWeightBytes()
	if !exact {
		expertTotal = g.estimateExpertBytesFromDims()
	}
	if expertTotal <= 0 {
		return OffloadPlan{Reason: "could not determine expert tensor sizes for this model"}
	}

	weights := g.WeightBytes()
	if weights <= 0 {
		return OffloadPlan{ExpertBytesTotal: expertTotal, Reason: "model weight size unknown; cannot compute a recommendation"}
	}
	kv := g.KVCacheBytes(ctxLen)
	overhead := (weights + kv) * 8 / 100 // ~8% for activations and compute buffers
	required := weights + kv + overhead

	plan := OffloadPlan{Applicable: true, ExpertBytesTotal: expertTotal}
	if required <= freeBytes {
		plan.FitsFullyOnGPU = true
		plan.Reason = "model fits fully in free VRAM; no offload needed"
		return plan
	}

	deficit := required - freeBytes
	layerCount := int(g.LayerCount)
	if layerCount <= 0 {
		return OffloadPlan{Applicable: true, ExpertBytesTotal: expertTotal,
			Reason: "layer count unknown; cannot compute a per-layer offload plan"}
	}

	// Walk layers from the front (--n-cpu-moe offloads the first N layers),
	// accumulating the VRAM each frees until the deficit is covered.
	var freed int64
	n := 0
	uniformPerLayer := expertTotal / int64(layerCount)
	for layer := 0; layer < layerCount; layer++ {
		n++
		if exact {
			freed += perLayer[layer]
		} else {
			freed += uniformPerLayer
		}
		if freed >= deficit {
			break
		}
	}

	plan.NCpuMoe = n
	if freed < deficit {
		plan.NCpuMoe = layerCount
		plan.Reason = "even offloading all expert layers may not fit free VRAM; consider a smaller quant, fewer GPU layers, or a shorter context"
		return plan
	}
	plan.Reason = "offload the experts of the first N layers to CPU so the rest fits in free VRAM"
	return plan
}
