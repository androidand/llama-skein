package fit

import "encoding/json"

// mlxDims is the subset of a Hugging Face config.json the fit math needs.
// Field names follow the transformers convention.
type mlxDims struct {
	NumHiddenLayers       int64 `json:"num_hidden_layers"`
	NumAttentionHeads     int64 `json:"num_attention_heads"`
	NumKeyValueHeads      int64 `json:"num_key_value_heads"`
	HiddenSize            int64 `json:"hidden_size"`
	HeadDim               int64 `json:"head_dim"`
	MaxPositionEmbeddings int64 `json:"max_position_embeddings"`
}

// mlxConfig is a config.json with the dims promoted from the top level plus a
// nested text_config. Multimodal / MoE models (e.g. qwen3_5_moe) leave the
// top-level dims empty and carry the real ones under text_config.
type mlxConfig struct {
	mlxDims
	TextConfig *mlxDims `json:"text_config"`
}

// ShapeFromMLXConfig builds a ModelShape from an MLX/HF model's config.json
// bytes and the resident weight byte total (summed safetensors). It prefers the
// nested text_config dims when present (the MoE layout), otherwise the top
// level. head_dim is taken explicitly when set, else derived from
// hidden_size / num_attention_heads. KV accounting uses only attention dims, so
// MoE expert count does not enter here — the experts show up only in
// weightBytes, which is the full resident weight set.
func ShapeFromMLXConfig(configJSON []byte, weightBytes int64) (ModelShape, error) {
	var c mlxConfig
	if err := json.Unmarshal(configJSON, &c); err != nil {
		return ModelShape{}, err
	}
	d := c.mlxDims
	if c.TextConfig != nil && c.TextConfig.NumHiddenLayers > 0 {
		d = *c.TextConfig
	}
	headDim := d.HeadDim
	if headDim == 0 && d.NumAttentionHeads > 0 {
		headDim = d.HiddenSize / d.NumAttentionHeads
	}
	return ModelShape{
		LayerCount:      d.NumHiddenLayers,
		EmbeddingLength: d.HiddenSize,
		KeyLength:       headDim,
		ValueLength:     headDim,
		HeadCount:       d.NumAttentionHeads,
		HeadCountKV:     d.NumKeyValueHeads,
		// MLX configs do not expose a hybrid full-attention interval; treat
		// every layer as holding KV (conservative — never under-counts memory).
		FullAttentionInterval: 0,
		WeightBytes:           weightBytes,
		TrainedCtx:            d.MaxPositionEmbeddings,
	}, nil
}
