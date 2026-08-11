// Package fit computes how a model fits a host: the memory footprint, the
// maximum safe context, and a fit verdict. It is the Go home of the engine
// absorbed from llmfit (MIT) — see openspec/changes/add-model-fit-engine.
//
// The core value over pkg/gguf's built-in estimates is cache-type awareness:
// gguf.KVCacheBytes assumes FP16 KV, but real deployments quantize the KV
// cache (--cache-type-k/v q8_0/q4_0), which shrinks it 2-3.5x and is the
// difference between a model fitting at 73k ctx or only 20k.
package fit

import (
	"fmt"
	"runtime"

	"github.com/androidand/llama-skein/internal/perf"
	"github.com/androidand/llama-skein/pkg/gguf"
)

// SystemSpecs is the fit-facing host hardware description built from the
// existing perf snapshot. It reuses vm_stat / kernel-pressure telemetry and
// GPU stats — no re-detection. The WiredLimitMB field is platform-specific:
// on macOS it is the iogpu.wired_limit_mb value (0 = OS default ~70% of RAM).
type SystemSpecs struct {
	Backend        string // "llamacpp", "mlx", "vllm", etc.
	UnifiedMemory  bool   // Apple Silicon unified pool
	VRAMTotalMB    int    // total VRAM / unified GPU budget
	VRAMFreeMB     int    // currently free VRAM (0 = unknown)
	RAMTotalMB     int    // total system RAM
	RAMAvailableMB int    // available for new allocations without paging
	Cores          int    // logical CPU cores
}

// FromSnapshot builds SystemSpecs from the latest perf snapshot and the
// configured backend. The wiredLimitMB is the macOS GPU wired-memory limit
// (0 on non-Apple platforms). GPU stats are deduplicated to one sample per
// GPU ID before aggregation.
func FromSnapshot(backend string, sysStats []perf.SysStat, gpuStats []perf.GpuStat, wiredLimitMB int) SystemSpecs {
	spec := SystemSpecs{
		Backend: backend,
		Cores:   runtime.NumCPU(),
	}
	if len(sysStats) == 0 {
		return spec
	}
	sys := sysStats[len(sysStats)-1]
	// Budget against the effective (cgroup-clamped) figures — in a container
	// the raw meminfo numbers can be the host's.
	spec.RAMTotalMB = sys.EffectiveMemTotalMB()
	spec.RAMAvailableMB = sys.EffectiveMemAvailableMB()
	if spec.RAMAvailableMB == 0 && sys.MemLimitSource == "" {
		spec.RAMAvailableMB = sys.MemFreeMB
	}

	unified := runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
	gpus := perf.LatestGPUs(gpuStats)
	if len(gpus) == 0 {
		if unified {
			// No discrete GPU stats — Apple Silicon with no ioreg overlay.
			// Budget is the wired limit (or ~70% of RAM).
			budget := unifiedBudgetMB(spec.RAMTotalMB, wiredLimitMB)
			spec.UnifiedMemory = true
			spec.VRAMTotalMB = budget
			spec.VRAMFreeMB = clamp0(budget - spec.RAMTotalMB + spec.RAMAvailableMB)
		}
		return spec
	}

	var totalMB, usedMB int
	for _, g := range gpus {
		totalMB += g.MemTotalMB
		usedMB += g.MemUsedMB
	}
	if unified {
		// GpuStat totals on Apple report the whole unified pool; used is the
		// GPU-attributed slice (ioreg overlay).
		budget := unifiedBudgetMB(totalMB, wiredLimitMB)
		free := budget - usedMB
		if spec.RAMAvailableMB > 0 && spec.RAMAvailableMB < free {
			free = spec.RAMAvailableMB
		}
		spec.UnifiedMemory = true
		spec.VRAMTotalMB = budget
		spec.VRAMFreeMB = clamp0(free)
		return spec
	}
	spec.VRAMTotalMB = totalMB
	spec.VRAMFreeMB = clamp0(totalMB - usedMB)
	return spec
}

func unifiedBudgetMB(totalRAM, wiredLimitMB int) int {
	if wiredLimitMB > 0 {
		return wiredLimitMB
	}
	return totalRAM * 70 / 100
}

func clamp0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

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
	ParallelSlots int     // --parallel/-np in the command; llama-server divides n_ctx across slots. 0/1 = no division
	// Unproven marks a model that has never loaded on this host (a
	// hypothetical/gallery candidate scored before download). A ConfiguredCtx
	// then means "requested ctx to score at", not "proven to load at" — so the
	// configured-model rescue from "no" and the under-configured flag are
	// disabled, and "no" stays "no".
	Unproven bool

	// CPU-offload placement, parsed from the launch command by the server
	// layer (offload translator + -ngl). Weights placed on CPU leave the
	// VRAM requirement and are budgeted against HostBudgetMB instead.
	CpuMoeAll bool // --cpu-moe: every layer's experts on CPU
	NCpuMoe   int  // --n-cpu-moe N: experts of the first N layers on CPU (0 = none)
	// NGpuLayers is the parsed -ngl value; nil = unset/auto/all (everything
	// the engine can place goes to GPU — upstream --fit may still adjust,
	// which this engine cannot model and treats as full-GPU, conservative).
	// An explicit 0 is CPU-only.
	NGpuLayers *int
	// HostBudgetMB is the effective host RAM available for CPU-resident
	// weights (cgroup-aware available minus reserves). 0 = unknown: host-side
	// feasibility is then not checked (fail-open, matching VRAM semantics).
	HostBudgetMB int

	// Companion artifact VRAM (in bytes). Non-zero when the model package
	// includes a draft model (--model-draft) or multimodal projector (--mmproj)
	// that reside on GPU alongside the main model. These are added to the
	// VRAM budget before computing KV cache headroom.
	DraftMB     int // draft model weights (e.g. DFlash drafter)
	ProjectorMB int // multimodal projector weights (e.g. mmproj)
}

// Result is the computed fit. It mirrors the apicontract.ModelFit fields the
// server returns, but stays free of the generated types so the engine is
// independently testable.
type Result struct {
	KVBytesPerToken  int64
	ModelMB          int
	ConfiguredCtx    int
	MaxSafeCtx       int // the context callers should trim PROMPTS to
	MaxFitCtx        int // largest --ctx-size (hard n_ctx) that fits VRAM; the grow target for an under-configured model. 0 when VRAM is unknown.
	KVMBAtMaxSafeCtx int
	VRAMRequiredMB   int
	VRAMTotalMB      int
	FitLevel         string // perfect | good | tight | marginal | no | unknown
	Reason           string
	// UnderConfigured is true when a configured model's --ctx-size is materially
	// below the context VRAM could safely hold (achievable ceiling). It surfaces
	// a starved config that would otherwise be invisible — the fit report echoes
	// the tiny configured_ctx and nothing signals the model is under-sized.
	UnderConfigured bool
	// Weight placement split. GPUResidentMB + HostResidentMB ≈ ModelMB;
	// HostResidentMB > 0 means the command offloads weights to CPU/system RAM
	// (hybrid placement) and VRAMRequiredMB covers only the GPU share.
	GPUResidentMB  int
	HostResidentMB int
	// RunMode is how the model runs given its command's placement flags:
	// gpu | moe_offload | cpu_offload | cpu_only (contract vocabulary).
	RunMode string
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
	// underConfigFrac flags a configured model as under-configured when its
	// --ctx-size sits below this fraction of the VRAM-achievable ceiling. Matches
	// skein's ctx-fit sweep grow threshold so the two agree on what "starved" is.
	underConfigFrac = 0.8
	// mtpExtraSafetyFrac applies an ADDITIONAL VRAM haircut (on top of
	// vramSafetyFrac) for self-speculative "nextn"/MTP models. Their draft head
	// needs its own KV-cache-like state and a larger transient compute buffer
	// during draft verification (batch-processing several candidate tokens at
	// once) — neither is modeled by kvBytesPerShape/computeOverheadFrac, which
	// only know about the base transformer stack. This is a conservative,
	// not-precisely-derived margin: root-caused after opencode-skein's
	// 413-triggered ctx auto-shrink repeatedly wrote a max_fit_ctx for
	// Qwythos-9B-MTP that still OOM'd on load (rocky, 2026-07). Revisit if
	// llama.cpp's MTP memory formula is characterized precisely.
	mtpExtraSafetyFrac = 0.85
)

// ModelShape is the backend-neutral set of dimensions the fit math needs. It
// lets the engine score llama.cpp GGUF models and MLX/safetensors models with
// the same code: GGUF fills it via ShapeFromGGUF, MLX via ShapeFromMLXConfig.
type ModelShape struct {
	LayerCount            int64
	EmbeddingLength       int64
	KeyLength             int64 // explicit K head_dim (0 = derive from embedding/heads)
	ValueLength           int64 // explicit V head_dim (0 = derive)
	HeadCount             int64
	HeadCountKV           int64
	FullAttentionInterval int64 // >1 = hybrid attention/SSM; only 1/interval layers hold KV
	WeightBytes           int64 // resident weight bytes
	TrainedCtx            int64 // max trained context (n_ctx_train / max_position_embeddings)
	// IsMTP is true when the model has a self-speculative "nextn"/MTP draft
	// head baked into its own weights. Its extra draft-verification KV/compute
	// overhead isn't otherwise modeled by this engine (LayerCount/kvBytesPerShape
	// only know about the base transformer stack) — see mtpExtraSafetyFrac.
	IsMTP bool
	// IsMoE + expert byte sizes power hybrid GPU/CPU placement math: expert
	// tensors moved to CPU (--cpu-moe / --n-cpu-moe) leave VRAM and are
	// budgeted against host RAM instead. ExpertBytesPerLayer is keyed by
	// layer index (nil when only a dimensional total estimate exists).
	IsMoE               bool
	ExpertBytesTotal    int64
	ExpertBytesPerLayer map[int]int64
}

// ShapeFromGGUF projects a parsed GGUF onto the neutral shape. WeightBytes
// already falls back to the file size for mmap'd weights; TrainedCtx is the
// model's trained default.
func ShapeFromGGUF(g *gguf.GGUF) ModelShape {
	// WeightBytes returns 0 when general.parameter_count is absent; the GGUF
	// file size is the resident weight size for mmap'd llama.cpp weights, so
	// fall back to it.
	weightBytes := g.WeightBytes()
	if weightBytes <= 0 {
		weightBytes = g.FileSize
	}
	shape := ModelShape{
		LayerCount:            g.LayerCount,
		EmbeddingLength:       g.EmbeddingLength,
		KeyLength:             g.KeyLength,
		ValueLength:           g.ValueLength,
		HeadCount:             g.HeadCount,
		HeadCountKV:           g.HeadCountKV,
		FullAttentionInterval: g.FullAttentionInterval,
		WeightBytes:           weightBytes,
		IsMTP:                 g.IsMTP(),
		IsMoE:                 g.IsMoE(),
		TrainedCtx:            g.MinCtxSize(),
	}
	if shape.IsMoE {
		if total, perLayer, ok := g.ExpertWeightBytes(); ok {
			shape.ExpertBytesTotal = total
			shape.ExpertBytesPerLayer = perLayer
		} else if est := g.EstimateExpertBytesFromDims(); est > 0 {
			shape.ExpertBytesTotal = est
		}
	}
	return shape
}

// KVBytesPerToken returns the KV-cache bytes consumed per context token, using
// the model's real GQA/MQA dimensions and the configured cache-type bit widths.
func KVBytesPerToken(g *gguf.GGUF, kBits, vBits float64) int64 {
	return kvBytesPerShape(ShapeFromGGUF(g), kBits, vBits)
}

func kvBytesPerShape(g ModelShape, kBits, vBits float64) int64 {
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

// cpuOffloadBytes computes the weight bytes the launch command places on the
// CPU: MoE expert tensors moved by --cpu-moe / --n-cpu-moe, plus the layer
// share excluded by an explicit -ngl below the layer count. The -ngl split is
// proportional — an approximation (embeddings/output live off-layer), kept
// conservative by applying it only to the non-expert remainder.
func cpuOffloadBytes(g ModelShape, p Params) int64 {
	var expertCPU int64
	if g.IsMoE && g.ExpertBytesTotal > 0 {
		switch {
		case p.CpuMoeAll:
			expertCPU = g.ExpertBytesTotal
		case p.NCpuMoe > 0:
			n := p.NCpuMoe
			if g.LayerCount > 0 && int64(n) > g.LayerCount {
				n = int(g.LayerCount)
			}
			if g.ExpertBytesPerLayer != nil {
				// --n-cpu-moe N offloads the experts of the FIRST N layers.
				for layer := 0; layer < n; layer++ {
					expertCPU += g.ExpertBytesPerLayer[layer]
				}
			} else if g.LayerCount > 0 {
				expertCPU = g.ExpertBytesTotal * int64(n) / g.LayerCount
			}
		}
	}
	rest := g.WeightBytes - expertCPU
	if rest < 0 {
		rest = 0
	}
	if p.NGpuLayers != nil && g.LayerCount > 0 && int64(*p.NGpuLayers) < g.LayerCount {
		ngl := int64(*p.NGpuLayers)
		if ngl < 0 {
			ngl = 0
		}
		expertCPU += rest * (g.LayerCount - ngl) / g.LayerCount
	}
	return expertCPU
}

// runMode names how the weights run given the command's placement flags,
// in the contract's run_mode vocabulary.
func runMode(p Params, gpuMB, hostMB int) string {
	switch {
	case hostMB <= 0:
		return "gpu"
	case gpuMB <= 0:
		return "cpu_only"
	case p.CpuMoeAll || p.NCpuMoe > 0:
		return "moe_offload"
	default:
		return "cpu_offload"
	}
}

// Analyze computes the fit of a model (parsed GGUF) on a host (Params).
func Analyze(g *gguf.GGUF, p Params) Result {
	return AnalyzeShape(ShapeFromGGUF(g), p)
}

// AnalyzeShape computes the fit of a backend-neutral model shape on a host. It
// is the real engine; Analyze adapts a GGUF onto it and the MLX path builds a
// shape from the model's config.json + safetensors sizes.
func AnalyzeShape(g ModelShape, p Params) Result {
	if p.KCacheBits == 0 {
		p.KCacheBits = 16
	}
	if p.VCacheBits == 0 {
		p.VCacheBits = 16
	}
	if p.OutputReserve == 0 {
		p.OutputReserve = defaultOutputReserve
	}

	kvPerTok := kvBytesPerShape(g, p.KCacheBits, p.VCacheBits)
	// WeightBytes is the resident weight size (GGUF file size for mmap'd
	// llama.cpp weights, summed safetensors for MLX); the shape builder applies
	// any fallback before we get here.
	modelMB := int(g.WeightBytes / mib)

	// Hybrid placement: weights the command moves to CPU leave the VRAM
	// requirement and are budgeted against host RAM below. gpuWeightMB is the
	// VRAM-side share all subsequent math uses.
	hostWeightMB := int(cpuOffloadBytes(g, p) / mib)
	if hostWeightMB > modelMB {
		hostWeightMB = modelMB
	}
	gpuWeightMB := modelMB - hostWeightMB

	res := Result{
		KVBytesPerToken: kvPerTok,
		ModelMB:         modelMB,
		ConfiguredCtx:   p.ConfiguredCtx,
		VRAMTotalMB:     p.VRAMTotalMB,
		GPUResidentMB:   gpuWeightMB,
		HostResidentMB:  hostWeightMB,
		RunMode:         runMode(p, gpuWeightMB, hostWeightMB),
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
		// Unknowable, not unfittable: report "unknown" (max_safe stays 0) rather
		// than "no", so callers don't confuse "VRAM telemetry warming up" with
		// "model doesn't fit" and don't trust a max_safe_ctx computed against a
		// budget we never had.
		res.FitLevel = "unknown"
		res.Reason = "host VRAM not yet available (performance monitor warming up)"
		return res
	}

	// Hard ctx ceiling from VRAM: how many tokens of KV fit alongside weights,
	// compute overhead, and the safety cap. Use free VRAM when known, else total.
	budgetMB := p.VRAMTotalMB
	if p.VRAMFreeMB > 0 {
		budgetMB = p.VRAMFreeMB + gpuWeightMB // free + the (GPU share of) weights we'll (re)place
		// "free + weights we'll place" is only correct when the free figure
		// already excludes a DIFFERENT resident model this one will evict —
		// then the weights genuinely land in newly-freed space. When nothing
		// else is resident (the common case: model is stopped, e.g. computing
		// its own fit, or about to cold-load), VRAMFreeMB already sits near
		// VRAMTotalMB, and adding modelMB on top inflates the budget past the
		// card's actual physical capacity. Regression (rocky, Qwythos-9B,
		// 2026-07): this let max_fit_ctx recommend 682664 for a host where the
		// SAME model's vram_required_mb at its already-configured 648192 was
		// already 30054MB against a 24560MB card — max_fit_ctx was
		// recommending something LARGER than an amount already proven to
		// overflow. Never let the budget exceed the hard physical total.
		if p.VRAMTotalMB > 0 && budgetMB > p.VRAMTotalMB {
			budgetMB = p.VRAMTotalMB
		}
	}
	usableMB := float64(budgetMB) * vramSafetyFrac
	if g.IsMTP {
		usableMB *= mtpExtraSafetyFrac
	}
	// Companion artifacts (DFlash drafter, mmproj projector) reside on GPU
	// alongside the main model. Subtract their VRAM before computing KV
	// cache headroom.
	companionMB := p.DraftMB + p.ProjectorMB
	kvBudgetMB := usableMB - float64(gpuWeightMB+companionMB)*(1+computeOverheadFrac)
	vramMaxCtx := 0
	if kvBudgetMB > 0 {
		vramMaxCtx = int(kvBudgetMB * mib / float64(kvPerTok))
	}

	// The hard n_ctx is the smaller of what's configured (or the model's trained
	// max) and what VRAM allows.
	configured := p.ConfiguredCtx > 0
	hardCtx := p.ConfiguredCtx
	if hardCtx <= 0 {
		hardCtx = int(g.TrainedCtx) // trained default
	}
	// Only second-guess the ctx against our VRAM estimate for a model that is NOT
	// already deployed. A configured_ctx comes from the running command — the
	// operator has proven it loads at that size — so capping it down from an
	// estimate (which we can't compute exactly for hybrid/offloaded models) would
	// understate the real ceiling.
	if !configured && vramMaxCtx > 0 && vramMaxCtx < hardCtx {
		hardCtx = vramMaxCtx
	}

	// llama-server splits n_ctx across --parallel slots: KV is allocated for
	// the full n_ctx (hardCtx, used for VRAM sizing below), but a single
	// request only ever gets n_ctx/slots. Advertising the undivided figure
	// made clients trim prompts to ~232k that a 4-slot server rejected at
	// 65k (z4, 2026-07-06).
	perReqCtx := hardCtx
	if p.ParallelSlots > 1 {
		perReqCtx /= p.ParallelSlots
	}

	// max_safe_ctx: prompt budget below the per-request ceiling, reserving
	// output room and a tokenizer-mismatch margin so a caller never ships a
	// prompt that 413s.
	safe := int(float64(perReqCtx)*promptMarginFrac) - p.OutputReserve
	if safe < 0 {
		safe = 0
	}
	res.MaxSafeCtx = safe
	res.KVMBAtMaxSafeCtx = int(kvPerTok * int64(safe) / mib)
	// VRAM requirement covers the GPU-resident share + companion artifacts.
	// KV stays on GPU (llama.cpp --kv-offload defaults on) and the compute
	// overhead follows the total GPU-resident weights.
	res.VRAMRequiredMB = gpuWeightMB + companionMB + int(float64(gpuWeightMB+companionMB)*computeOverheadFrac) + int(kvPerTok*int64(hardCtx)/mib)

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
	case vramMaxCtx >= int(g.TrainedCtx):
		res.FitLevel = "perfect"
		res.Reason = "fits the full trained context with headroom"
	default:
		res.FitLevel = "good"
		res.Reason = "fits comfortably at this context"
	}

	// Host-side feasibility for hybrid placement: CPU-resident weights must
	// fit the effective host budget (when known). This is the check upstream
	// llama.cpp's --fit does not perform — it happily overflows into system
	// RAM the cgroup will OOM-kill. Unknown budget fails open, matching the
	// VRAM semantics above.
	if hostWeightMB > 0 && p.HostBudgetMB > 0 && hostWeightMB > p.HostBudgetMB {
		res.FitLevel = "no"
		res.Reason = fmt.Sprintf("CPU-offloaded weights (%d MB) exceed the safe host memory budget (%d MB)", hostWeightMB, p.HostBudgetMB)
	}

	// Safety net: a configured (deployed) model demonstrably loads at this ctx.
	// Our VRAM estimate can still be conservative for architectures we don't model
	// exactly (hybrid SSM/attention, MoE/CPU offload), so never DISCARD a running
	// model as "no" — the worst a deployed model earns is "marginal". This is the
	// guarantee that the engine never rejects a model that actually fits.
	if configured && !p.Unproven && res.FitLevel == "no" {
		res.FitLevel = "marginal"
		res.Reason = "runs at the configured context; VRAM estimate exceeds budget (architecture not fully modeled or memory offloaded)"
	}

	// A model that does not fit has no usable prompt budget: never advertise a
	// max_safe_ctx computed from a hard ctx the host can't honor (this is what
	// emitted max_safe_ctx≈237k alongside fit_level:"no"). Only unconfigured
	// models can still be "no" here — the configured safety net above rescues
	// deployed ones to "marginal".
	if res.FitLevel == "no" {
		res.MaxSafeCtx = 0
		res.KVMBAtMaxSafeCtx = 0
	}

	// The largest hard ctx that fits VRAM — the grow target skein's sweep patches
	// an under-configured model up to. 0 when VRAM is unknown (vramMaxCtx == 0),
	// so the sweep knows not to grow blindly.
	if vramMaxCtx > 0 {
		res.MaxFitCtx = vramMaxCtx
	}

	// Flag a configured model whose --ctx-size is materially below what VRAM
	// could hold, so the starved config is visible (skein's ctx-fit sweep grows
	// it; the model-load path warns). MaxFitCtx is the VRAM-achievable ceiling.
	if configured && !p.Unproven && res.MaxFitCtx > 0 && p.ConfiguredCtx < int(float64(res.MaxFitCtx)*underConfigFrac) {
		res.UnderConfigured = true
		res.Reason = fmt.Sprintf("%s; configured ctx %d is well below the ~%d this host could hold", res.Reason, p.ConfiguredCtx, res.MaxFitCtx)
	}
	return res
}
