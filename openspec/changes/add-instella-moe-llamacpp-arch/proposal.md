# Add Instella-MoE architecture support to llama.cpp

## Context

Instella-MoE is a DeepSeek-V3 derivative in which **five of six** unusual features are
already implemented upstream: MLA with a null `q_lora_rank` (the `is_lite` path, which
coincidentally triggers on `n_layer() == 27` — exactly Instella's layer count,
`src/models/deepseek2.cpp:8`), sigmoid `noaux_tc` routing with `exp_probs_b`, shared
experts, interleaved RoPE (`LLM_ARCH_DEEPSEEK2` → `LLAMA_ROPE_TYPE_NORM`), and the YaRN
`mscale_all_dim` correction. **Gated attention is also done** — `LLM_TENSOR_ATTN_GATE`
exists and `src/models/afmoe.cpp` implements `attn_out * sigmoid(gate)` before `o_proj`
identically to Instella.

**Exactly one feature is missing: FarSkip.** It carries two residual streams across each
layer boundary and runs attention and the MoE FFN as a parallel block. Per
`modeling_instella_moe.py` and arXiv:2511.11505, writing `x_k` for the full stream and
`x̃_k` for the routed-free stream:

```
a_k = Attn(LN1(x̃_k))                    # attention reads the ROUTED-FREE stream
r_k = x_k + a_k
(routed, shared) = MoE(LN2(x_k))         # MoE reads the PRE-attention stream
x_{k+1} = r_k + routed + shared
x̃_{k+1} = r_k + shared                   # = x_{k+1} − routed
```

Layer 0 is FarSkip and dense, receives a plain tensor, still a parallel block. Layer 1 also
single-stream. Layers 2–26 two-stream. `output_norm` consumes the full stream only.

Full analysis: `docs/investigations/instella-moe-w7800.md` §3.4, §4, §6.

**This change targets a llama.cpp fork, not this repo.**

## Why

FarSkip requires **one extra activation tensor and zero new ggml operators**. llama.cpp
already has two precedents for multi-stream residuals — DeepSeek-V4 hyper-connections
(`src/models/deepseek4.cpp`, `inpL` as `[n_embd, hc_mult, n_tokens]`) and Gemma 3n AltUp
(`src/models/gemma3n.cpp`, 4 streams). FarSkip is a strict special case of the former: two
streams, fixed routing, no learned mixing matrix.

FarSkip's *purpose* is to overlap MoE all-to-all with compute in a distributed setting — on
a single GPU there is no collective and no speed benefit. But its architectural residue
must be reproduced exactly, because the weights were self-distilled into this topology and
are not convertible back to a standard MoE.

## What changes

`LLM_ARCH_INSTELLA_MOE` plus a new `src/models/instella-moe.cpp` forked from
`deepseek2.cpp`, threading two residual tensors and adding the attention gate. No CMake
change needed — `src/CMakeLists.txt:9` globs `models/*.cpp`.

Note `docs/development/HOWTO-add-model.md` is **stale**: it still places graphs in
`src/llama-model.cpp` and never mentions `src/models/` or `tests/test-llama-archs.cpp`.

## Non-goals

- The converter — see `add-instella-moe-gguf-conversion`.
- Numerical validation — see `add-instella-moe-correctness-harness`.
- The missing flash-attention kernel at head_dim 544 — see `add-fattn-mla-544-head-dim`.
  Instella runs correctly without FA; this is a performance matter only.

## Risks

- **A subtly wrong FarSkip produces fluent but incorrect output.** This is the central
  technical risk of the whole effort. Mitigated by the staged A/B/C oracles in
  `add-instella-moe-correctness-harness`, which exploit the model's own
  `attn_only_farskip` / `mlp_only_farskip` config flags to decompose the two changes.
- **`build_moe_ffn` may not expose the routed sum separately from the shared sum.** The
  graph needs them separately. Inspect before writing; this is assumed, not verified.
- Forcing the weights through as `deepseek2` fails **loudly** (`attn_gate` is emitted but
  never requested → "wrong number of tensors", `llama-model-loader.cpp:1317`), which is a
  useful accidental safety net. Do not remove that property by making `attn_gate`
  optional.
