package fit

// Descriptor describes a model that is not on disk — a gallery/catalog
// candidate scored before download. Explicit dims win when the catalog knows
// them (e.g. from the model's config.json); otherwise the engine estimates a
// dense-transformer shape from the parameter count and reports estimated=true.
type Descriptor struct {
	ParamsB    float64 // parameter count in billions (used when dims are absent)
	ArchFamily string  // informational; recorded for the caller, not yet used in estimation

	// Optional explicit dims. Any zero field falls back to estimation.
	LayerCount      int64
	EmbeddingLength int64
	HeadCount       int64
	HeadCountKV     int64
	HeadDim         int64 // explicit K/V head_dim (0 = derive from embedding/heads)
	TrainedCtx      int64
}

// densePreset anchors the estimation table to real dense checkpoints
// (qwen2.5-0.5b … llama3.1-70b). MoE models do not follow this table —
// callers should pass explicit dims for them; the estimated flag warns either
// way.
type densePreset struct {
	maxParamsB float64
	layers     int64
	hidden     int64
	heads      int64
	kvHeads    int64
}

var densePresets = []densePreset{
	{0.7, 24, 896, 14, 2},
	{1.8, 28, 1536, 12, 2},
	{2.5, 28, 2048, 16, 4},
	{4.5, 32, 2560, 20, 4},
	{9, 32, 4096, 32, 8},
	{16, 44, 5120, 40, 8},
	{25, 48, 6144, 48, 8},
	{40, 64, 5120, 40, 8},
	{75, 80, 8192, 64, 8},
	{1e18, 96, 12288, 96, 8},
}

// estimatedTrainedCtx is the trained-context assumption when the descriptor
// does not say: 32k is the floor of the current model generation, and a low
// guess only caps MaxFitCtx conservatively — it never inflates a verdict.
const estimatedTrainedCtx = 32768

// ShapeFromDescriptor builds a ModelShape for a hypothetical model.
// weightBytes is the resident weight size of the quant variant being scored
// (GGUF or summed-safetensors file size). estimated reports whether any core
// dimension had to be guessed from ParamsB rather than supplied explicitly.
func ShapeFromDescriptor(d Descriptor, weightBytes int64) (shape ModelShape, estimated bool) {
	shape = ModelShape{
		LayerCount:      d.LayerCount,
		EmbeddingLength: d.EmbeddingLength,
		HeadCount:       d.HeadCount,
		HeadCountKV:     d.HeadCountKV,
		KeyLength:       d.HeadDim,
		ValueLength:     d.HeadDim,
		TrainedCtx:      d.TrainedCtx,
		WeightBytes:     weightBytes,
	}
	if shape.LayerCount <= 0 || shape.EmbeddingLength <= 0 || shape.HeadCount <= 0 {
		preset := densePresets[len(densePresets)-1]
		for _, p := range densePresets {
			if d.ParamsB <= p.maxParamsB {
				preset = p
				break
			}
		}
		estimated = true
		if shape.LayerCount <= 0 {
			shape.LayerCount = preset.layers
		}
		if shape.EmbeddingLength <= 0 {
			shape.EmbeddingLength = preset.hidden
		}
		if shape.HeadCount <= 0 {
			shape.HeadCount = preset.heads
		}
		if shape.HeadCountKV <= 0 {
			shape.HeadCountKV = preset.kvHeads
		}
	}
	if shape.HeadCountKV <= 0 {
		// Explicit dims without a KV-head count: assume GQA-8 (near-universal
		// in the current generation) rather than full MHA, which would
		// overestimate KV several-fold and reject models that fit.
		shape.HeadCountKV = min64(8, shape.HeadCount)
		estimated = true
	}
	if shape.TrainedCtx <= 0 {
		shape.TrainedCtx = estimatedTrainedCtx
		estimated = true
	}
	return shape, estimated
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
