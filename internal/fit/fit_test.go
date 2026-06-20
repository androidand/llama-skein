package fit

import (
	"testing"

	"github.com/androidand/llama-skein/pkg/gguf"
)

func TestBitsPerElement(t *testing.T) {
	cases := map[string]float64{"": 16, "f16": 16, "q8_0": 8.5, "q4_0": 4.5, "f32": 32, "weird": 16}
	for in, want := range cases {
		if got := BitsPerElement(in); got != want {
			t.Errorf("BitsPerElement(%q) = %v, want %v", in, got, want)
		}
	}
}

// q4_0 KV must be ~3.5x smaller per token than FP16 — the whole reason the
// cache-type-aware math exists.
func TestKVBytesPerToken_CacheTypeScaling(t *testing.T) {
	g := &gguf.GGUF{LayerCount: 64, HeadCount: 40, HeadCountKV: 8, EmbeddingLength: 5120}
	f16 := KVBytesPerToken(g, 16, 16)
	q4 := KVBytesPerToken(g, 4.5, 4.5)
	if f16 <= 0 || q4 <= 0 {
		t.Fatalf("got non-positive: f16=%d q4=%d", f16, q4)
	}
	ratio := float64(f16) / float64(q4)
	if ratio < 3.4 || ratio > 3.6 {
		t.Errorf("f16/q4_0 KV ratio = %.2f, want ~3.55", ratio)
	}
}

// Calibration: a ~35B-A3B-class MoE at Q4 with q4_0 KV must reach the known-good
// ~73k context on a 36 GB unified Mac — the M3 config that works in production.
// Arch params approximate Qwen3-class 35B (exact values come from the live GGUF
// at runtime; this pins the math's ballpark).
func TestAnalyze_Calibration_35B_Q4_Unified36GB(t *testing.T) {
	g := &gguf.GGUF{
		Architecture: "qwen3moe", LayerCount: 64, HeadCount: 40, HeadCountKV: 8,
		EmbeddingLength: 5120, ParamCount: 35_000_000_000, FileSize: 21_000_000_000,
		ExpertCount: 128, ExpertUsedCount: 8,
	}
	// q4_0 KV, no explicit --ctx-size (use trained max), 36 GB unified.
	res := Analyze(g, Params{
		KCacheBits: 4.5, VCacheBits: 4.5,
		ConfiguredCtx: 73728,
		VRAMTotalMB:   36864,
		VRAMFreeMB:    36864 - 20000, // model resident
	})
	if res.FitLevel == "no" {
		t.Fatalf("35B Q4 with q4_0 KV must fit on 36GB, got %q: %s", res.FitLevel, res.Reason)
	}
	// max_safe_ctx should be a usable fraction below the configured 73728.
	if res.MaxSafeCtx < 50000 || res.MaxSafeCtx >= 73728 {
		t.Errorf("max_safe_ctx = %d, want in [50000, 73728) for the working M3 config", res.MaxSafeCtx)
	}
	t.Logf("35B/q4_0/36GB: fit=%s max_safe_ctx=%d kv/tok=%dB model=%dMB",
		res.FitLevel, res.MaxSafeCtx, res.KVBytesPerToken, res.ModelMB)
}

// max_safe_ctx must always sit below the hard configured ctx (the qwopus 413
// class: callers trimming to max_safe_ctx must never reach n_ctx).
func TestAnalyze_MaxSafeCtxBelowConfigured(t *testing.T) {
	g := &gguf.GGUF{LayerCount: 48, HeadCount: 32, HeadCountKV: 8, EmbeddingLength: 4096,
		ParamCount: 18_000_000_000, FileSize: 19_000_000_000}
	res := Analyze(g, Params{KCacheBits: 8.5, VCacheBits: 8.5, ConfiguredCtx: 65536, VRAMTotalMB: 24576})
	if res.MaxSafeCtx >= 65536 {
		t.Errorf("max_safe_ctx %d must be below configured n_ctx 65536", res.MaxSafeCtx)
	}
	if res.MaxSafeCtx <= 0 {
		t.Errorf("max_safe_ctx should be positive, got %d", res.MaxSafeCtx)
	}
}

func TestAnalyze_InsufficientMetadata(t *testing.T) {
	res := Analyze(&gguf.GGUF{}, Params{VRAMTotalMB: 24576})
	if res.FitLevel != "no" {
		t.Errorf("expected fit=no with no arch metadata, got %q", res.FitLevel)
	}
}
