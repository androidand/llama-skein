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

// Regression (qwopus-MTP): a Qwen3-Next-style HYBRID model — sparse full-attention
// layers (full_attention_interval=4) interleaved with SSM layers, explicit
// head_dim=256 — that runs fine at 86k on a 32 GB GPU must NOT be reported
// fit_level:"no". The old math counted KV over all 65 blocks with head_dim
// 5120/24=213, inflating VRAM to ~38.7 GB and discarding a model that fits.
// Values are the live GGUF's (Qwopus3.6-27B-v2-MTP-Q8_0) with q8_0 KV.
func TestAnalyze_Hybrid_Qwopus_NotDiscarded(t *testing.T) {
	g := &gguf.GGUF{
		Architecture: "qwen35", LayerCount: 65, HeadCount: 24, HeadCountKV: 4,
		EmbeddingLength: 5120, KeyLength: 256, ValueLength: 256,
		FullAttentionInterval: 4, ContextLength: 262144,
		FileSize: 27701 * 1024 * 1024, // ~27.7 GB Q8 weights
	}
	res := Analyze(g, Params{
		KCacheBits: BitsPerElement("q8_0"), VCacheBits: BitsPerElement("q8_0"),
		ConfiguredCtx: 86016,
		VRAMTotalMB:   32624,
	})
	if res.FitLevel == "no" {
		t.Fatalf("hybrid model that runs at 86k on 32GB must not be discarded; got %q: %s", res.FitLevel, res.Reason)
	}
	// Prompt ceiling must sit just below the configured n_ctx — the value that
	// actually prevents the 413 (≈ 86016*0.92 - 4096).
	if res.MaxSafeCtx < 60000 || res.MaxSafeCtx >= 86016 {
		t.Errorf("max_safe_ctx = %d, want in [60000, 86016)", res.MaxSafeCtx)
	}
	// Sanity: hybrid KV must be far below the all-65-layers @ head_dim 213 figure
	// (~117k B/tok) that caused the false "no".
	if res.KVBytesPerToken <= 0 || res.KVBytesPerToken > 80_000 {
		t.Errorf("KV/tok = %d B, expected hybrid (attention-layers only) to be well below the dense count", res.KVBytesPerToken)
	}
	t.Logf("qwopus hybrid: fit=%s max_safe_ctx=%d kv/tok=%dB vram_req=%dMB",
		res.FitLevel, res.MaxSafeCtx, res.KVBytesPerToken, res.VRAMRequiredMB)
}

// Regression (rocky, Qwythos-9B-MTP, 2026-07): opencode-skein's 413-triggered
// auto-shrink patches a model's --ctx-size down to max_fit_ctx whenever a
// prompt overflows, trusting it as a safe ceiling. For a self-speculative
// "nextn"/MTP model this repeatedly still OOM'd on the very next load — the
// draft head's own KV-cache-like state and its larger transient
// draft-verification compute buffer aren't modeled by kvBytesPerShape or the
// flat computeOverheadFrac, which only know about the base transformer stack.
// An MTP-flagged shape must report a strictly smaller MaxFitCtx/MaxSafeCtx
// than an otherwise-identical non-MTP shape at the same VRAM budget — the
// extra margin (mtpExtraSafetyFrac) is actually being applied.
func TestAnalyzeShape_MTP_GetsExtraSafetyMargin(t *testing.T) {
	base := ModelShape{
		LayerCount: 65, HeadCount: 24, HeadCountKV: 4,
		EmbeddingLength: 5120, KeyLength: 256, ValueLength: 256,
		WeightBytes: 9786060736, // ~9.3 GB, Qwythos-9B-Q8's real weight size
		TrainedCtx:  1000000,
	}
	params := Params{
		KCacheBits: BitsPerElement("q8_0"), VCacheBits: BitsPerElement("q8_0"),
		VRAMTotalMB: 24576, // a 24GB card, matching the class of host that hit this
	}

	nonMTP := base
	nonMTP.IsMTP = false
	mtp := base
	mtp.IsMTP = true

	resNonMTP := AnalyzeShape(nonMTP, params)
	resMTP := AnalyzeShape(mtp, params)

	if resNonMTP.MaxFitCtx <= 0 || resMTP.MaxFitCtx <= 0 {
		t.Fatalf("expected both to compute a positive MaxFitCtx, got non-MTP=%d MTP=%d", resNonMTP.MaxFitCtx, resMTP.MaxFitCtx)
	}
	if resMTP.MaxFitCtx >= resNonMTP.MaxFitCtx {
		t.Errorf("MTP MaxFitCtx (%d) must be strictly smaller than the non-MTP figure (%d) — the extra safety margin isn't being applied",
			resMTP.MaxFitCtx, resNonMTP.MaxFitCtx)
	}
	if resMTP.MaxSafeCtx >= resNonMTP.MaxSafeCtx {
		t.Errorf("MTP MaxSafeCtx (%d) must be strictly smaller than the non-MTP figure (%d)", resMTP.MaxSafeCtx, resNonMTP.MaxSafeCtx)
	}
	t.Logf("non-MTP: max_fit_ctx=%d max_safe_ctx=%d | MTP: max_fit_ctx=%d max_safe_ctx=%d",
		resNonMTP.MaxFitCtx, resNonMTP.MaxSafeCtx, resMTP.MaxFitCtx, resMTP.MaxSafeCtx)
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

// VRAM telemetry not yet available is "unknown" (can't tell), not "no" (doesn't
// fit); max_safe_ctx must be 0 so callers don't trust a budget with no basis.
func TestAnalyze_VRAMUnavailable_ReportsUnknown(t *testing.T) {
	g := &gguf.GGUF{LayerCount: 48, HeadCount: 32, HeadCountKV: 8, EmbeddingLength: 4096,
		ParamCount: 18_000_000_000, FileSize: 19_000_000_000}
	res := Analyze(g, Params{ConfiguredCtx: 32768}) // no VRAMTotalMB/VRAMFreeMB
	if res.FitLevel != "unknown" {
		t.Errorf("expected fit=unknown when VRAM unavailable, got %q", res.FitLevel)
	}
	if res.MaxSafeCtx != 0 {
		t.Errorf("max_safe_ctx must be 0 when fit is unknown, got %d", res.MaxSafeCtx)
	}
}

// Regression: an UNCONFIGURED model whose native context can't fit VRAM used to
// emit a huge max_safe_ctx (~237k) alongside fit_level:"no". A non-fitting model
// has no usable prompt budget — max_safe_ctx must be 0.
func TestAnalyze_NoFit_ClampsMaxSafeToZero(t *testing.T) {
	g := &gguf.GGUF{LayerCount: 48, HeadCount: 32, HeadCountKV: 8, EmbeddingLength: 4096,
		ContextLength: 262144, FileSize: 30_000 * 1024 * 1024} // ~30GB weights
	res := Analyze(g, Params{VRAMTotalMB: 24576}) // ConfiguredCtx 0 (unconfigured), weights > VRAM
	if res.FitLevel != "no" {
		t.Fatalf("expected fit=no (weights exceed VRAM), got %q: %s", res.FitLevel, res.Reason)
	}
	if res.MaxSafeCtx != 0 {
		t.Errorf("max_safe_ctx must be 0 when fit=no, got %d", res.MaxSafeCtx)
	}
}

// A configured model pinned far below the VRAM-achievable ceiling is flagged
// under_configured (the z4 --ctx-size 3072 on a 48GB card class); an in-range
// config is not.
func TestAnalyze_UnderConfigured(t *testing.T) {
	shape := ModelShape{LayerCount: 48, EmbeddingLength: 4096, HeadCount: 32, HeadCountKV: 8,
		WeightBytes: 8 << 30, TrainedCtx: 262144}
	host := Params{VRAMTotalMB: 64 << 10, VRAMFreeMB: 48 << 10}

	starved := host
	starved.ConfiguredCtx = 3072
	r := AnalyzeShape(shape, starved)
	if !r.UnderConfigured {
		t.Errorf("configured 3072 with a large achievable ceiling should be under_configured (fit=%s)", r.FitLevel)
	}
	// MaxFitCtx is the grow target skein's sweep uses; it must exceed the starved config.
	if r.MaxFitCtx <= starved.ConfiguredCtx {
		t.Errorf("max_fit_ctx %d must exceed the starved configured ctx %d", r.MaxFitCtx, starved.ConfiguredCtx)
	}

	roomy := host
	roomy.ConfiguredCtx = 200000 // near the achievable ceiling
	if r := AnalyzeShape(shape, roomy); r.UnderConfigured {
		t.Errorf("configured near the achievable ceiling must not be under_configured")
	}
}

// llama-server divides n_ctx across --parallel slots; max_safe_ctx must
// reflect the per-request share while VRAM sizing keeps the full allocation.
func TestAnalyze_ParallelSlotsDividePerRequestCtx(t *testing.T) {
	shape := ModelShape{
		LayerCount:      48,
		EmbeddingLength: 4096,
		HeadCount:       32,
		HeadCountKV:     8,
		WeightBytes:     8 << 30, // 8 GiB
		TrainedCtx:      262144,
	}
	base := Params{ConfiguredCtx: 262144, VRAMTotalMB: 64 << 10, VRAMFreeMB: 48 << 10}

	solo := AnalyzeShape(shape, base)

	quad := base
	quad.ParallelSlots = 4
	split := AnalyzeShape(shape, quad)

	if solo.MaxSafeCtx <= 0 || split.MaxSafeCtx <= 0 {
		t.Fatalf("expected positive max_safe_ctx, got solo=%d split=%d", solo.MaxSafeCtx, split.MaxSafeCtx)
	}
	// Per-request budget shrinks by the slot count (margins/reserve make it
	// slightly non-linear; assert the divided ceiling drives the result).
	wantCeil := 262144/4*92/100 + 1
	if split.MaxSafeCtx >= solo.MaxSafeCtx/2 || split.MaxSafeCtx > wantCeil {
		t.Fatalf("split.MaxSafeCtx = %d, want well below solo (%d) and <= per-slot ceiling %d",
			split.MaxSafeCtx, solo.MaxSafeCtx, wantCeil)
	}
	// The KV allocation (VRAM requirement) is for the FULL n_ctx: unchanged.
	if split.VRAMRequiredMB != solo.VRAMRequiredMB {
		t.Fatalf("VRAMRequiredMB changed with slots: %d vs %d", split.VRAMRequiredMB, solo.VRAMRequiredMB)
	}
}

// Regression (rocky, Qwythos-9B, 2026-07): "free + weights we'll place" only
// makes sense when free already excludes a DIFFERENT resident model this one
// will evict. With nothing else resident (the model itself is stopped —
// exactly opencode-skein's 413-recovery scenario, which queries /api/fit for
// a not-currently-loaded model), VRAMFreeMB already sits near VRAMTotalMB, so
// adding modelMB on top let the computed budget exceed the card's actual
// physical capacity. Concretely: rocky's real numbers were VRAMTotalMB=24560,
// VRAMFreeMB≈24512 (idle), ModelMB=9332 — the old formula's budget
// (24512+9332=33844) is 38% over the real 24560MB ceiling, which is exactly
// why max_fit_ctx (682664) recommended something LARGER than the 648192
// already independently shown to require 30054MB against the same 24560MB
// card. MaxFitCtx must never imply a budget bigger than VRAMTotalMB.
func TestAnalyzeShape_BudgetNeverExceedsPhysicalVRAM(t *testing.T) {
	shape := ModelShape{
		LayerCount: 65, HeadCount: 24, HeadCountKV: 4,
		EmbeddingLength: 5120, KeyLength: 256, ValueLength: 256,
		WeightBytes: 9786060736, // ~9.3 GB, Qwythos-9B-Q8's real weight size
		TrainedCtx:  1000000,
	}
	res := AnalyzeShape(shape, Params{
		KCacheBits: BitsPerElement("q8_0"), VCacheBits: BitsPerElement("q8_0"),
		VRAMTotalMB: 24560,
		VRAMFreeMB:  24512, // idle — nothing else resident, matching rocky's actual state
	})

	if res.MaxFitCtx <= 0 {
		t.Fatal("expected a positive MaxFitCtx")
	}
	// The KV+compute a recommended MaxFitCtx implies must fit within
	// VRAMTotalMB alongside the model's own weights — i.e. re-deriving
	// VRAMRequiredMB AT max_fit_ctx must not exceed the physical card.
	modelMB := int(shape.WeightBytes / mib)
	kvPerTok := KVBytesPerToken(&gguf.GGUF{
		LayerCount: shape.LayerCount, HeadCount: shape.HeadCount, HeadCountKV: shape.HeadCountKV,
		EmbeddingLength: shape.EmbeddingLength, KeyLength: shape.KeyLength, ValueLength: shape.ValueLength,
	}, BitsPerElement("q8_0"), BitsPerElement("q8_0"))
	impliedRequiredMB := modelMB + int(float64(modelMB)*computeOverheadFrac) + int(kvPerTok*int64(res.MaxFitCtx)/mib)
	if impliedRequiredMB > 24560 {
		t.Errorf("MaxFitCtx=%d implies %dMB required — exceeds the 24560MB physical card (the exact rocky OOM pattern)",
			res.MaxFitCtx, impliedRequiredMB)
	}
}
