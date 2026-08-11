# Muse Glimmer 30B GGUF Research Report

## Summary

Meta has released Muse Glimmer 30B (~29.6B parameters) as a multimodal agentic model supporting vision input. The GGUF quantizations are available from both `meta-models` (official) and `unsloth` on HuggingFace. The model uses the `muse-glimmer` architecture with 131k context, hybrid attention (local/global), and ships as a **model package** with companion files for multimodal input (mmproj) and speculative decoding (DFlash).

---

## Architecture

Source: https://huggingface.co/meta-models/Muse-Glimmer-30B-GGUF (official model card)

| Property | Value |
|----------|-------|
| Architecture | Dense Causal Transformer with Perception Encoder |
| Total Parameters | ~29.6B (including vision encoder) |
| Hidden dimension | 6656 |
| Layers | 52 |
| Attention pattern | [Local, Local, Local, Global] repeating |
| Sliding window size | 2048 |
| Gated attention | Yes |
| Attention heads (Q / KV) | 32 / 2 (GQA ratio 16:1) |
| Head dimension | 128 |
| FFN type | SwiGLU |
| FFN intermediate dimension | 19,968 |
| Position encoding | RoPE (θ = 500,000), local layers only |
| Perception encoder | ~1.8B param ViT-G/14, 50 layers, width 1536, patch size 14 |
| Vocabulary size | 202,048 (200k BPE + 2k special) |
| Context length | 131,072+ |
| Supported modalities | Input: text + image, Output: text |

### Key Architecture Notes

- **Hybrid attention**: Only 1 in 4 layers is global (FullAttentionInterval = 4). The other 3 are local with sliding window 2048. This significantly affects KV cache sizing — only global layers carry growing KV cache.
- **Aggressive GQA**: 16:1 ratio (32 query heads, 2 KV heads) — KV cache is much smaller than a standard transformer.
- **Perception encoder**: Separate ViT-G/14 model (~1.8B params) embedded or loaded alongside the language model for multimodal input.

---

## Model Package Structure

Muse Glimmer ships as a **model package** with three artifacts:

```
Muse Glimmer 30B
├── main GGUF (weights)
├── dflash-kquant.gguf (DFlash drafter)
└── mmproj-kquant.gguf (perception encoder)
```

### Companion Files

| File | Description | Required |
|------|-------------|----------|
| `dflash-kquant.gguf` | DFlash draft model for speculative decoding | Optional (speeds up generation) |
| `mmproj-kquant.gguf` | Quantized perception encoder for image input | Required for multimodal |

**Discovery mechanism**: Companion files are NOT referenced via GGUF metadata. They must be discovered by:
1. HuggingFace repository metadata (sibling files in the same repo)
2. Filename conventions (e.g., `dflash-*.gguf`, `mmproj-*.gguf`)
3. Explicit specification by the caller

---

## Quantizations

### meta-models/Muse-Glimmer-30B-GGUF (Official)

Meta provides **two** K-quant variants optimized for different VRAM envelopes:

| File | Target VRAM | Degradation | Size |
|------|-------------|-------------|------|
| `muse-glimmer-30B-kquant-dynamic.gguf` | 32 GB | 0.2% | 19.7 GB |
| `muse-glimmer-30B-kquant-17gb.gguf` | 24 GB | 1.0% | 16.8 GB |
| `dflash-kquant.gguf` | — | — | 1.63 GB |
| `mmproj-kquant.gguf` | — | — | 1.4 GB |

Source: https://huggingface.co/meta-models/Muse-Glimmer-30B-GGUF

### unsloth/Muse-Glimmer-30B-GGUF (Unsloth)

Unsloth provides **many more** quantization variants using their Dynamic 2.0 quantization:

| Quantization | Size | Notes |
|-------------|------|-------|
| UD-IQ2_XXS | 10.7 GB | 2-bit |
| UD-IQ2_XS | 11.5 GB | 2-bit |
| UD-IQ2_M | 12.3 GB | 2-bit |
| UD-Q2_K_XL | 12.4 GB | 2-bit |
| UD-IQ3_XXS | 13.1 GB | 3-bit |
| UD-Q3_K_XL | 13.4 GB | 3-bit |
| UD-IQ3_M | 14.1 GB | 3-bit |
| **UD-Q4_K_XL** | **15.9 GB** | **4-bit (recommended default)** |
| UD-Q5_K_M | 19.2 GB | 5-bit |
| UD-Q5_K_XL | 21.8 GB | 5-bit |
| UD-Q6_K_XL | 26.3 GB | 6-bit |
| Q8_0 | 29.6 GB | 8-bit |
| UD-Q8_K_XL | 32.3 GB | 8-bit |
| BF16 | 55.7 GB | 16-bit |

Source: https://huggingface.co/unsloth/Muse-Glimmer-30B-GGUF

**Unsloth is NOT a mirror** — it provides significantly more quantization options than Meta's official repo.

### Quantization Choice for W7800 (48 GB VRAM)

For the AMD Radeon PRO W7800 with 48 GB VRAM:
- **Best quality**: `Q8_0` (29.6 GB) or `UD-Q6_K_XL` (26.3 GB) — fits with ample room for KV cache + companions
- **Meta official**: `kquant-dynamic` (~28 GB) — designed for 32 GB, fits easily on 48 GB
- **Not recommended**: Q4 variants (15.9 GB) — unnecessarily low quality for 48 GB VRAM

---

## DFlash Speculative Decoding

### What is DFlash?

DFlash (arXiv:2602.06036) is a **block diffusion speculative decoding** method. Unlike traditional speculative decoding that uses a smaller autoregressive draft model, DFlash uses a diffusion-based process to propose entire blocks of 16 tokens simultaneously.

### DFlash Specs

| Component | Setting |
|-----------|---------|
| Draft layers | 5 |
| Block size | 16 |
| Attention | Sliding-window, 2048, all layers |
| Attention heads | 32 query / 8 KV (GQA) |
| Sequence length | 131,072 |
| Hidden-feature layers | 5, uniform over target: {1, 13, 25, 37, 49} of 52 |

### Performance

| GPU | Baseline (tok/s) | With DFlash (tok/s) | Speedup |
|-----|------------------|---------------------|---------|
| Nvidia RTX 5090 | 74.9 | 233.4 | 3.1x |
| Apple M4 Max | 23.7 | 37.8 | 1.5x |
| Apple M5 Max | 26.6 | 50.2 | 1.8x |

### llama.cpp Support

DFlash support has been merged into llama.cpp main via three PRs:
1. **PR #26577** — Fixed the `wo_a` reshape on load for DFlash models
2. **PR #26636** — Added backends of the other (draft) model to the context
3. **PR #26814** — Added auto-detection of spec type from draft GGUF metadata

llama.cpp can auto-detect DFlash from the draft model's GGUF metadata. The draft model should be passed via `--model-draft` flag.

---

## GGUF Metadata

Source: https://huggingface.co/meta-models/Muse-Glimmer-30B-GGUF

### Architecture & Core

| Key | Value |
|-----|-------|
| `general.architecture` | `muse-glimmer` |
| `general.name` | `Muse-Glimmer-30B` |
| `general.file_type` | varies by quant |
| `general.parameter_count` | ~29.6B |
| `general.context_length` | `131072` (131k) |
| `general.quantization_version` | `2` |

### Muse-Glimmer Architecture Keys

| Key | Value |
|-----|-------|
| `muse-glimmer.block_count` | `52` |
| `muse-glimmer.embedding_length` | `6656` |
| `muse-glimmer.feed_forward_length` | `19968` |
| `muse-glimmer.attention.head_count` | `32` |
| `muse-glimmer.attention.head_count_kv` | `2` |
| `muse-glimmer.attention.key_length` | `128` |
| `muse-glimmer.attention.value_length` | `128` |
| `muse-glimmer.attention.layer_norm_rms_epsilon` | `1e-05` |
| `muse-glimmer.rope.freq_base` | `500000.0` |
| `muse-glimmer.attention.sliding_window` | `2048` |
| `muse-glimmer.full_attention_interval` | `4` |

### Chat Template

The model includes a custom chat template (stored in `general.chat_template`) that supports:
- **Vision patches**: `<|patch|>` tokens for image inputs
- **Tool calling**: ATEM function call tokens

---

## Context Length

The GGUF metadata declares `muse-glimmer.context_length` = 131072. However, the reference
Reddit configuration overrides this to 262144 via:

```bash
--override-kv muse-glimmer.context_length=int:262144,dflash.context_length=int:262144
```

This suggests the model can technically support 262144 tokens, but the GGUF header only
announces 131072. llama.cpp's `--override-kv` mechanism is needed to access the full context.

**Implication for llama-skein**: The fit engine reads `muse-glimmer.context_length` from
GGUF metadata (131072). If a user requests 262144 context, the system must either:
1. Support `--override-kv` in runtime command construction, or
2. Accept that the GGUF-declared context is the safe default and let users override via
   explicit `--ctx-size` (which llama.cpp will then apply against the override-kv values).

The DFlash drafter also has `dflash.context_length` metadata that should match the main
model's context when using `--override-kv`.

## Best Practices (from Meta)

**Sampling Parameters:**
- temperature = 1.0
- top_p = 0.95
- top_k = 64

**Reasoning Strength:** Use `Reasoning strength: high` or `Reasoning strength: xhigh` in system prompt for complex problem solving, coding, and agentic tasks.

---

## Risks and Unknowns

- **Hybrid attention**: The [Local, Local, Local, Global] pattern means FullAttentionInterval = 4. KV cache sizing must account for this — only 1 in 4 layers holds growing KV.
- **Companion discovery**: No standard mechanism for automatic companion file discovery. Must be handled by the model manager or client.
- **Chat template**: The custom chat template with `<|patch|>` and ATEM tokens requires careful integration.
- **llama.cpp version**: DFlash support requires a recent llama.cpp build with the merged PRs.

---

## Distributed Recipe System

There is no single `inference_recipe.json` on HuggingFace, but a substantial part of the recipe can be reconstructed deterministically from standardized repository files. This is a **distributed recipe system** across multiple machine-readable layers.

### Machine-Readable Recipe Layers

#### Layer 1: Canonical HF Config Files

Muse Glimmer's canonical repository contains:

```
config.json                 # Architecture / dimensions / model type
generation_config.json      # Generation defaults (temperature, top_p, top_k, etc.)
tokenizer_config.json       # Conversation protocol / special tokens
chat_template.jinja         # Chat template (Jinja2 format)
processor_config.json       # Multimodal preprocessing stack
tokenizer.json              # Tokenizer files
```

**`generation_config.json`** is particularly important — HuggingFace Transformers explicitly loads generation parameters from this file when present. It can contain defaults for temperature, top_k, top_p, beam settings, repetition penalties, token IDs, and other generation behavior.

**`tokenizer_config.json`** and/or **`chat_template.jinja`** carry the chat protocol. HuggingFace defines chat templates as part of the tokenizer model and supports tool schemas through those templates.

**`processor_config.json`** plus the processor abstraction describes the multimodal preprocessing stack (text tokenizers, image processors, etc.).

#### Layer 2: GGUF Metadata

GGUF files contain a self-describing header with:
- Architecture identifier
- Hyperparameters (hidden_size, num_layers, attention heads, etc.)
- Quantization information
- Context length
- Chat template (embedded in `general.chat_template`)
- Tokenizer metadata

This is the llama.cpp-specific architecture representation.

#### Layer 3: HF Repository File Tree

The repository file listing reveals:
- Main weights (GGUF files)
- Companion files: `mmproj-*.gguf`, `dflash-*.gguf`, `mtp-*.gguf`
- Quantization variants
- Split GGUFs (multi-file models)

#### Layer 4: llama.cpp's HF Resolver Conventions

llama.cpp has its own knowledge about HuggingFace repository conventions:

**Current capabilities:**
- `llama-server -hf user/repository[:quant]` — downloads main GGUF + auto-downloads mmproj if available
- `--hf-repo-draft user/repository[:quant]` — downloads draft model for speculative decoding
- `--no-mmproj` — disables automatic mmproj download

**Critical discovery (July 17, 2026):**
llama.cpp PR #25811 — "auto-download dflash- and eagle3- HF sidecars"
- Merged into llama.cpp main (commit `635cdd5`)
- Mirrors the existing `mtp-` sidecar logic to support DFlash and Eagle3
- Adds `--dflash` and `--eagle3` CLI flags to trigger sidecar download
- Adds `find_best_dflash()` and `find_best_eagle3()` using `find_best_sibling`
- Excludes `dflash-` and `eagle3-` filenames from primary model selection
- Filters these from cached model listings
- Wires download tasks that set `speculative.draft.mparams` as fallback

This means llama.cpp already contains a **recipe database** — just not called one. The logic for discovering and downloading model companions is already implemented and maintained upstream.

#### Layer 5: HF Hub API

`HfApi.model_info()` exposes:
- Repository file entries (siblings)
- File sizes / LFS metadata
- Model card metadata (YAML frontmatter)

This allows cheap inspection before downloading large files.

### Model Recipe vs Hardware Tuning

The key insight is separating **model recipe** (inherent to the model) from **hardware tuning** (specific to the deployment target).

**Model Recipe** (from canonical HF files + GGUF metadata):
```yaml
capabilities:
  vision: true
  tools: true
  reasoning: true

companions:
  perception:
    file: mmproj-kquant.gguf
  speculative:
    type: dflash
    file: dflash-kquant.gguf

generation:
  temperature: 1.0
  top_p: 0.95
  top_k: 64

chat:
  template: <jinja2 template>

context:
  native: 131072
```

**Hardware Tuning** (from VRAM measurement + GPU profile):
```yaml
context: 262144
kv:
  k: f16
  v: f16

gpu_layers: all
flash_attention: true

speculative:
  enabled: true
  gpu_layers: all
```

### Recommended Approach: Structured-First, Runtime-Informed, Heuristic-Last

**Priority order:**

1. **Canonical HF config files** — config.json, generation_config.json, tokenizer/chat template, processor configs
2. **GGUF metadata** — runtime architecture, quantization, tokenizer, context, etc.
3. **HF repository topology** — discover mmproj, dflash, MTP, split GGUFs, quant variants
4. **llama.cpp's own HF resolver/conventions** — do not duplicate logic it already reliably owns
5. **Hardware measurement/tuning** — VRAM fit, KV precision, offload, tensor split, flash attention
6. **Model-card structured metadata** — YAML frontmatter for discovery
7. **README/model-card extraction** — only for things not represented above
8. **Curated overrides** — only for genuine exceptions/bugs/workarounds

### Architectural Implication for llama-skein

**Delegate to llama.cpp where possible.**

As of July 17, 2026, upstream llama.cpp already knows how to auto-download mmproj, DFlash, and Eagle3 sidecars from HF. This is almost exactly the functionality we're currently designing.

The most Skein-ish solution may be: **let llama.cpp be the authoritative model-runtime recipe engine where possible**, while llama-skein adds:
- Hardware fitting (VRAM measurement, quantization selection)
- Lifecycle management (pull, cache, cleanup)
- Observability (health, metrics, logs)
- Model selection (user-facing queries, filtering)
- Overrides (when llama.cpp's defaults are wrong)

This avoids maintaining a second half-copy of llama.cpp's rapidly evolving model knowledge.

---

## W7800 (48 GB) VRAM Budget

For the AMD Radeon PRO W7800 with 48 GB VRAM, using `kquant-dynamic` (19.7 GB):

| Component | VRAM (GB) |
|-----------|-----------|
| Main weights (kquant-dynamic) | 19.7 |
| DFlash drafter | 1.63 |
| mmproj (if loaded) | 1.4 |
| KV cache (F16, 131k ctx, hybrid attention) | ~4-6 |
| Runtime buffers / overhead | ~2-3 |
| **Total at 131k ctx, no mmproj** | ~27-28 |
| **Total at 131k ctx, with mmproj** | ~28-30 |
| **Total at 262k ctx, no mmproj** | ~31-33 |
| **Total at 262k ctx, with mmproj** | ~32-34 |

This leaves ~14-16 GB headroom even at 262k context with mmproj loaded. F16 KV cache is
comfortably feasible. The kquant-dynamic variant is the best choice for this hardware —
much better quality than Q4 at only ~4 GB more than kquant-17gb.

## Recommendations

1. **For W7800 48 GB**: Use `kquant-dynamic` (19.7 GB) from Meta. Leaves ~18 GB for KV cache, DFlash, mmproj, and runtime buffers. F16 KV at 262k context fits comfortably.
2. **For DFlash**: Download `dflash-kquant.gguf` from the Meta repo. llama.cpp auto-detects from draft GGUF metadata (PR #26814). Pass via `--model-draft`.
3. **For multimodal**: Download `mmproj-kquant.gguf` from the Meta repo. Pass via `--mmproj`. Only load when image input is needed.
4. **Model package**: Treat Muse Glimmer as a package (main + dflash + mmproj), not a single GGUF. The operation system's multi-artifact support (ArtifactRole: weights, projector, other) handles this generically.
5. **Context length**: GGUF declares 131072. For 262144, use `--override-kv muse-glimmer.context_length=int:262144,dflash.context_length=int:262144`.
6. **Architecture**: Use structured-first approach. Let llama.cpp handle HF resolver conventions. llama-skein adds hardware fitting, lifecycle, observability, and overrides around it.
7. **Test first**: Before implementing custom package resolution, test what current llama.cpp does with `llama serve -hf meta-models/Muse-Glimmer-30B-GGUF`.
