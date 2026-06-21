package fit

import "testing"

// TestShapeFromMLXConfig_NestedMoE verifies the qwen3_5_moe layout (dims nested
// under text_config, top-level keys absent) is parsed and yields a sane,
// protective max_safe_ctx — the case that was crashing mlx_lm with a Metal OOM.
func TestShapeFromMLXConfig_NestedMoE(t *testing.T) {
	cfg := []byte(`{
		"model_type": "qwen3_5_moe",
		"text_config": {
			"num_hidden_layers": 40,
			"num_attention_heads": 16,
			"num_key_value_heads": 2,
			"hidden_size": 2048,
			"head_dim": 256,
			"max_position_embeddings": 262144
		},
		"vision_config": {"hidden_size": 1152}
	}`)
	const weightBytes = int64(20391679439) // ~19 GB resident safetensors

	shape, err := ShapeFromMLXConfig(cfg, weightBytes)
	if err != nil {
		t.Fatalf("ShapeFromMLXConfig: %v", err)
	}
	if shape.LayerCount != 40 || shape.HeadCountKV != 2 || shape.KeyLength != 256 {
		t.Errorf("dims: got layers=%d kvHeads=%d headDim=%d, want 40/2/256",
			shape.LayerCount, shape.HeadCountKV, shape.KeyLength)
	}
	if shape.TrainedCtx != 262144 {
		t.Errorf("trained ctx = %d, want 262144", shape.TrainedCtx)
	}
	if shape.WeightBytes != weightBytes {
		t.Errorf("weight bytes = %d, want %d", shape.WeightBytes, weightBytes)
	}

	// At the default GPU wired limit (~25.8 GB on a 36 GB Mac) the model fits a
	// few tens of k of context — well below the 84k+ that was OOM-crashing it.
	res := AnalyzeShape(shape, Params{VRAMTotalMB: 25800}) // f16 KV (MLX default)
	if res.MaxSafeCtx <= 0 {
		t.Fatalf("expected a positive max_safe_ctx, got %d (%s)", res.MaxSafeCtx, res.Reason)
	}
	if res.MaxSafeCtx >= 84000 {
		t.Errorf("max_safe_ctx = %d at ~25.8GB budget; expected it below the 84k that crashed", res.MaxSafeCtx)
	}
	t.Logf("MLX qwen3_5_moe: fit=%s max_safe_ctx=%d kv/tok=%dB model=%dMB",
		res.FitLevel, res.MaxSafeCtx, res.KVBytesPerToken, res.ModelMB)
}
