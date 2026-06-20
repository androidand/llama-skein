// Package fit computes how a model fits a host: the memory footprint, the
// maximum safe context, and a fit verdict. It is the Go home of the engine
// absorbed from llmfit (MIT) — see openspec/changes/add-model-fit-engine.
//
// The core value over pkg/gguf's built-in estimates is cache-type awareness:
// gguf.KVCacheBytes assumes FP16 KV, but real deployments quantize the KV
// cache (--cache-type-k/v q8_0/q4_0), which shrinks it 2-3.5x and is the
// difference between a model fitting at 73k ctx or only 20k.
package fit

import "github.com/androidand/llama-skein/pkg/gguf"

// BitsPerElement returns the bits used per KV-cache element for a llama.cpp
// cache-type flag value. Defaults to FP16 (16) for unknown/empty types, which
// is llama.cpp's own default when --cache-type-* is unset.
func BitsPerElement(cacheType string) float64 {
	switch cacheType {
	case "f32":
		return 32
	case "f16", "bf16", "":
		return 16
	case "q8_0":
		return 8.5 // 8-bit quants + per-block scale
	case "q5_0", "q5_1":
		return 6
	case "q4_0", "q4_1":
		return 4.5 // 4-bit quants + per-block scale
	default:
		return 16
	}
}

// Params describes the host budget and the runtime KV configuration the model
// is (or would be) launched with. Bits come from the --cache-type-k/v flags via
// BitsPerElement; the server layer parses the command.
type Params struct {
	KCacheBits    float64 // bits per K element (BitsPerElement of --cache-type-k)
	VCacheBits    float64 // bits per V element (BitsPerElement of --cache-type-v)
	ConfiguredCtx int     // --ctx-size in the command, 0 = model trained default
	OutputReserve int     // tokens to keep free for generation (e.g. --n-predict); default applied if 0
	VRAMTotalMB   int     // total VRAM / unified pool
	VRAMFreeMB    int     // currently free VRAM (for a live max-ctx estimate)
}

// Result is the computed fit. It mirrors the apicontract.ModelFit fields the
// server returns, but stays free of the generated types so the engine is
// independently testable.
type Result struct {
	KVBytesPerToken  int64
	ModelMB          int
	ConfiguredCtx    int
	MaxSafeCtx       int // the context callers should trim PROMPTS to
	KVMBAtMaxSafeCtx int
	VRAMRequiredMB   int
	VRAMTotalMB      int
	FitLevel         string // perfect | good | tight | marginal | no
	Reason           string
}

const (
	// computeOverheadFrac reserves VRAM for activations/compute graphs beyond
	// weights+KV — gguf uses 5%; we keep parity.
	computeOverheadFrac = 0.05
	// vramSafetyFrac caps usable VRAM below the physical ceiling so a loaded
	// model never sits at ~100% (fragile: compute spikes / MTP draft slots OOM).
	vramSafetyFrac = 0.92
	// promptMarginFrac trims max_safe_ctx below the hard n_ctx so a caller's
	// token count (a different tokenizer than the model's) overshooting slightly
	// does not exceed n_ctx — the qwopus 413 class. ~8% headroom.
	promptMarginFrac     = 0.92
	defaultOutputReserve = 4096
	mib                  = 1024 * 1024
)

// KVBytesPerToken returns the KV-cache bytes consumed per context token, using
// the model's real GQA/MQA dimensions and the configured cache-type bit widths.
func KVBytesPerToken(g *gguf.GGUF, kBits, vBits float64) int64 {
	if g.LayerCount <= 0 || g.EmbeddingLength <= 0 {
		return 0
	}
	// head_dim: prefer the model's explicit key/value_length. Qwen3-family models
	// set head_dim independently of embedding_length/head_count (e.g. 256 vs the
	// 213 the division would imply), so the classic ratio is only a fallback.
	kHeadDim := g.KeyLength
	vHeadDim := g.ValueLength
	if kHeadDim <= 0 || vHeadDim <= 0 {
		derived := g.EmbeddingLength
		if g.HeadCount > 0 {
			derived = g.EmbeddingLength / g.HeadCount
		}
		if kHeadDim <= 0 {
			kHeadDim = derived
		}
		if vHeadDim <= 0 {
			vHeadDim = derived
		}
	}
	kvHeads := g.HeadCountKV
	if kvHeads <= 0 {
		kvHeads = g.HeadCount
	}
	if kvHeads <= 0 {
		return 0
	}
	// Only full-attention layers hold a growing KV cache. In a hybrid model
	// (FullAttentionInterval > 1, e.g. Qwen3-Next) the other blocks are linear/SSM
	// layers with a fixed recurrent state and no per-token KV — counting all
	// block_count layers as attention overestimates KV by the interval factor and
	// is what made a fitting model (qwopus-MTP) report fit_level:"no".
	attnLayers := g.LayerCount
	if g.FullAttentionInterval > 1 {
		attnLayers = g.LayerCount / g.FullAttentionInterval
		if attnLayers < 1 {
			attnLayers = 1
		}
	}
	// per attention layer per token: K (kvHeads*kHeadDim) + V (kvHeads*vHeadDim).
	bytesPerLayer := (float64(kvHeads*kHeadDim)*kBits + float64(kvHeads*vHeadDim)*vBits) / 8.0
	return int64(bytesPerLayer * float64(attnLayers))
}

// Analyze computes the fit of a model (parsed GGUF) on a host (Params).
func Analyze(g *gguf.GGUF, p Params) Result {
	if p.KCacheBits == 0 {
		p.KCacheBits = 16
	}
	if p.VCacheBits == 0 {
		p.VCacheBits = 16
	}
	if p.OutputReserve == 0 {
		p.OutputReserve = defaultOutputReserve
	}

	kvPerTok := KVBytesPerToken(g, p.KCacheBits, p.VCacheBits)
	// WeightBytes returns 0 when general.parameter_count is absent from the
	// metadata; the GGUF file size is the resident weight size for llama.cpp
	// (the whole file is mmap'd), so use it as the fallback.
	modelBytes := g.WeightBytes()
	if modelBytes <= 0 {
		modelBytes = g.FileSize
	}
	modelMB := int(modelBytes / mib)

	res := Result{
		KVBytesPerToken: kvPerTok,
		ModelMB:         modelMB,
		ConfiguredCtx:   p.ConfiguredCtx,
		VRAMTotalMB:     p.VRAMTotalMB,
	}
	if kvPerTok <= 0 || modelMB <= 0 {
		res.FitLevel = "no"
		res.Reason = "insufficient model metadata to estimate fit (missing layer/embedding counts or model size)"
		return res
	}
	// Without a VRAM figure the fit is unknowable — report that rather than
	// computing against a zero budget (which yields a nonsensical "exceeds
	// VRAM" with a huge max_safe_ctx).
	if p.VRAMTotalMB <= 0 && p.VRAMFreeMB <= 0 {
		res.FitLevel = "no"
		res.Reason = "host VRAM not yet available (performance monitor warming up)"
		return res
	}

	// Hard ctx ceiling from VRAM: how many tokens of KV fit alongside weights,
	// compute overhead, and the safety cap. Use free VRAM when known, else total.
	budgetMB := p.VRAMTotalMB
	if p.VRAMFreeMB > 0 {
		budgetMB = p.VRAMFreeMB + modelMB // free + the weights we'll (re)place
	}
	usableMB := float64(budgetMB) * vramSafetyFrac
	kvBudgetMB := usableMB - float64(modelMB)*(1+computeOverheadFrac)
	vramMaxCtx := 0
	if kvBudgetMB > 0 {
		vramMaxCtx = int(kvBudgetMB * mib / float64(kvPerTok))
	}

	// The hard n_ctx is the smaller of what's configured (or the model's trained
	// max) and what VRAM allows.
	configured := p.ConfiguredCtx > 0
	hardCtx := p.ConfiguredCtx
	if hardCtx <= 0 {
		hardCtx = int(g.MinCtxSize()) // trained default
	}
	// Only second-guess the ctx against our VRAM estimate for a model that is NOT
	// already deployed. A configured_ctx comes from the running command — the
	// operator has proven it loads at that size — so capping it down from an
	// estimate (which we can't compute exactly for hybrid/offloaded models) would
	// understate the real ceiling.
	if !configured && vramMaxCtx > 0 && vramMaxCtx < hardCtx {
		hardCtx = vramMaxCtx
	}

	// max_safe_ctx: prompt budget below hardCtx, reserving output room and a
	// tokenizer-mismatch margin so a caller never ships a prompt that 413s.
	safe := int(float64(hardCtx)*promptMarginFrac) - p.OutputReserve
	if safe < 0 {
		safe = 0
	}
	res.MaxSafeCtx = safe
	res.KVMBAtMaxSafeCtx = int(kvPerTok * int64(safe) / mib)
	res.VRAMRequiredMB = modelMB + int(float64(modelMB)*computeOverheadFrac) + int(kvPerTok*int64(hardCtx)/mib)

	// Fit verdict from headroom of the hard ctx against VRAM.
	switch {
	case res.VRAMRequiredMB > p.VRAMTotalMB:
		res.FitLevel = "no"
		res.Reason = "model + KV at this context exceeds VRAM"
	case float64(res.VRAMRequiredMB) > usableMB:
		res.FitLevel = "marginal"
		res.Reason = "fits only above the VRAM safety margin; reduce context"
	case res.VRAMRequiredMB > int(usableMB)*9/10:
		res.FitLevel = "tight"
		res.Reason = "fits with little headroom"
	case vramMaxCtx >= int(g.MinCtxSize()):
		res.FitLevel = "perfect"
		res.Reason = "fits the full trained context with headroom"
	default:
		res.FitLevel = "good"
		res.Reason = "fits comfortably at this context"
	}

	// Safety net: a configured (deployed) model demonstrably loads at this ctx.
	// Our VRAM estimate can still be conservative for architectures we don't model
	// exactly (hybrid SSM/attention, MoE/CPU offload), so never DISCARD a running
	// model as "no" — the worst a deployed model earns is "marginal". This is the
	// guarantee that the engine never rejects a model that actually fits.
	if configured && res.FitLevel == "no" {
		res.FitLevel = "marginal"
		res.Reason = "runs at the configured context; VRAM estimate exceeds budget (architecture not fully modeled or memory offloaded)"
	}
	return res
}
