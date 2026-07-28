# Add Instella-MoE GGUF conversion

## Context

`amd/Instella-MoE-16B-A3B-Think` (revision `e67a4a54d81b19692ec85ea1d1c777aa5c0bfd83`)
cannot be converted to GGUF today. llama.cpp's converter registry keys off
`config.json → architectures[0]` (`conversion/base.py:1109`), and
`InstellaMoEForCausalLM` is absent from all 196 `@ModelBase.register(...)` names, so
conversion fails cleanly with no partial output.

The model declares `"model_type": "deepseek_v3"` and is a genuine DeepSeek-V3 derivative.
Its tensor layout is close enough to `DeepseekV2Model` that the converter is a thin
subclass: the `kv_b_proj` → `attn_k_b`/`attn_v_b` split, the 3-D expert stacking, and the
sigmoid/`noaux_tc` routing metadata are all existing machinery.

Full analysis: `docs/investigations/instella-moe-w7800.md` §6.

**This change targets a llama.cpp fork, not this repo.** It is tracked here because
llama-skein is the deployment vehicle and this is where the ecosystem's planning lives.

## Why

Conversion is separable from architecture support and can be fully validated **before any
C++ exists** — tensor count, shapes, metadata keys, and tokenizer round-trip are all
checkable against the safetensors index without running inference. Landing it first means
that when the graph work begins, any mismatch is unambiguously in the graph.

## What changes

A new `conversion/instella.py` registering `InstellaMoEModel(DeepseekV2Model)` on the
**`architectures` name, not `model_type`** — `model_type` says `deepseek_v3` and
registering on it would silently capture real DeepSeek-V3 checkpoints.

Emits 484 GGUF tensors (down from 5,344 HF tensors; 4,992 expert tensors collapse into 78
3-D tensors) and **zero new GGUF metadata keys**. `farskip` and `gated_attention` are
implied by the architecture rather than stored — all six released checkpoints set both
true, and a `farskip: false` checkpoint would simply *be* `deepseek2`.

## Non-goals

- Architecture / graph support — see `add-instella-moe-llamacpp-arch`.
- Numerical validation against a reference — see `add-instella-moe-correctness-harness`.
- Quantization beyond producing F16 (F16 is what the correctness work needs; it fits in
  48 GB VRAM with full 32K context, so quantization loss stays separable from conversion
  error).

## Risks

- **The `mscale` trap.** `rope_scaling.mscale_all_dim = 1.0` is truthy, so the effective
  attention scale is `128**-0.5 * mscale**2 = 0.16562688`, **1.874×** the naive
  `1/sqrt(128)`. The converter must emit `rope.scaling.yarn_log_multiplier` such that
  llama.cpp's existing `[TAG_DEEPSEEK2_YARN_LOG_MUL_FIX]` handling
  ([PR #17945](https://github.com/ggml-org/llama.cpp/pull/17945)) reproduces it. Getting
  this wrong yields fluent-but-wrong output.
- **Tokenizer added-token flags.** `<|User|>`, `<|Assistant|>` and the tool markers are
  `special: false, normalized: true` — a known DeepSeek quirk that trips converters.
- The pre-tokenizer is byte-identical to `LLAMA_VOCAB_PRE_TYPE_DEEPSEEK3_LLM`
  (`src/llama-vocab.cpp:318-325`), so only a **hash registration** is needed. If the hash
  is registered wrong, tokenization silently diverges.
