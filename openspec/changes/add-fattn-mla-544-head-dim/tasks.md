# Tasks: Instantiate the flash-attention kernel for MLA head_dim 544

Target repo: **llama.cpp fork**, for upstream submission. Independent of the Instella arch
work — can land before, after, or without it.

## Phase 1 — confirm the diagnosis
- [ ] 1. Locate the `switch (K->ne[0])` in `ggml_cuda_get_best_fattn_kernel`
      (`ggml/src/ggml-cuda/fattn.cu`) and record the exact instantiated set on the current
      base commit.
- [ ] 2. Confirm `544` is absent and `576` is present.
- [ ] 3. Confirm empirically that an MLA model with `kv_lora_rank + qk_rope_head_dim = 544`
      reports flash attention as unavailable under `-fa auto`, and that `-fa on` either
      errors or falls back. Capture the log line.
- [ ] 4. Check whether the CPU / Vulkan / Metal backends have the same gap, and scope the
      change to the backends where `<576, 512>` already exists.

## Phase 2 — implement
- [ ] 5. Add the `<544, 512>` instantiation alongside `<576, 512>`.
- [ ] 6. Build for gfx1100 and confirm compile time / binary size impact is acceptable;
      record both.
- [ ] 7. Add a `test-backend-ops` case for head_dim 544 / v 512.

## Phase 3 — validate
- [ ] 8. Confirm FA is now selected for the 544 case.
- [ ] 9. **Correctness first**: compare logits with FA on vs FA off for the same model and
      prompt. They must agree within FA's normal tolerance. A speedup with wrong numbers is
      not a win.
- [ ] 10. Benchmark prompt processing at 1K / 4K / 8K / 16K / 32K, FA on vs off, on gfx1100.
      **Report measured numbers only** — note that on RDNA3 the selected kernel is the tile
      path, not WMMA (`Q->ne[0] > 128`), so do not assume a WMMA-sized gain.
- [ ] 11. If the measured gain is negligible on gfx1100, say so plainly in the PR and
      justify the change on other backends (or withdraw it).

## Phase 4 — upstream
- [ ] 12. Open the PR referencing the dimension arithmetic
      (`kv_lora_rank 512 + qk_rope_head_dim 32`) and naming at least one real model that
      needs it.
- [ ] 13. Disclose that the motivating model (`amd/Instella-MoE-16B-A3B-Think`) is
      research-licensed and not yet supported upstream — reviewers will ask, and leading
      with it is better than being asked.
