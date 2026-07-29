# Instantiate the flash-attention kernel for MLA head_dim 544

## Context

llama.cpp's `ggml_cuda_get_best_fattn_kernel` (`ggml/src/ggml-cuda/fattn.cu`) selects a
flash-attention kernel by switching on `K->ne[0]`, instantiated only for
`{40, 64, 72, 80, 96, 112, 128, 192, 256, 320, 512, 576}`. Anything else returns
`BEST_FATTN_KERNEL_NONE`, and `-fa auto` (the default, `common/arg.cpp:1666`) then silently
disables flash attention.

For a DeepSeek-style MLA model with absorption, the cached K dimension is
`kv_lora_rank + qk_rope_head_dim`. DeepSeek-V3 uses `512 + 64 = 576`, which **is**
instantiated. Any model with `qk_rope_head_dim = 32` gives `512 + 32 = **544**`, which is
**not**.

`amd/Instella-MoE-16B-A3B-Think` is exactly this case: `kv_lora_rank: 512`,
`qk_rope_head_dim: 32`, `v_head_dim: 128` → `K->ne[0] = 544`, `V->ne[0] = 512`.

Discovered during `docs/investigations/instella-moe-w7800.md` §7.

**This change targets a llama.cpp fork, for upstream submission.**

## Why

This is independent of, and more broadly useful than, Instella support:

- It is a **one-line kernel instantiation** plus a test case.
- It benefits **every** MLA model with `qk_rope_head_dim = 32`, not just Instella. That
  dimension is a natural choice for smaller MLA models and will recur.
- Output is already correct without it — this is purely a long-context performance
  recovery, which makes it low-risk to submit and low-risk to defer.
- It is worth landing even if the Instella work is abandoned, which is why it is separated
  from `add-instella-moe-llamacpp-arch` rather than folded into it.

## What changes

Add `<544, 512>` to the flash-attention kernel instantiation set, on the backends where
the neighbouring `<576, 512>` case is already instantiated.

## Non-goals

- Any other missing head-dimension combination. Add them when a real model needs one.
- Changing the RDNA3 WMMA head-dim cap. That cap (WMMA/`MMA_F16` selected on RDNA3 only
  when `Q->ne[0] <= 128`, falling back to `BEST_FATTN_KERNEL_TILE` above) is the current
  mitigation for the RDNA3 WMMA instability at large head dimensions, and is deliberately
  left alone — this repo's own `fix-rdna3-flash-attn-wedge` history is why.

## Risks

- Compile time and binary size grow with each instantiation; upstream may reasonably push
  back on adding a dimension with no widely-used model behind it. Instella-MoE being
  research-only weakens the case. Be prepared for this to be declined, and note that the
  fork can carry it regardless.
- On RDNA3 specifically, `Q->ne[0]` for this model exceeds 128, so the tile kernel is what
  gets selected — the benefit on gfx1100 comes from the tile path becoming *available*
  rather than from WMMA. **Measure before claiming a speedup.**
