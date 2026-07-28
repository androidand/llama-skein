# Tasks: Add Instella-MoE architecture support to llama.cpp

Target repo: **llama.cpp fork**. Blocked by `add-instella-moe-gguf-conversion` (needs a
GGUF to load). Template: [PR #24260 cohere2-MoE](https://github.com/ggml-org/llama.cpp/pull/24260)
— +632/−7 across 13 files, of which `src/models/cohere2moe.cpp` was +443.

## Phase 0 — prove the build before adding variables
- [ ] 1. On z4 LXC 102: `pct resize 102 rootfs +200G` first (99.3 GiB free is not enough
      for the full run; `rpool` has 779 G).
- [ ] 2. Clone llama.cpp at the pinned base commit into the container. Record the SHA.
- [ ] 3. Build **unmodified** for gfx1100:
      ```
      HIPCXX="$(hipconfig -l)/clang" HIP_PATH="$(hipconfig -R)" \
        cmake -S . -B build -DGGML_HIP=ON -DGPU_TARGETS=gfx1100 \
              -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=ON \
              -DLLAMA_SERVER_SSL=OFF -DLLAMA_BUILD_UI=OFF -DLLAMA_BUILD_WEBUI=OFF
      cmake --build build --config Release -t llama-server -j 8
      ```
- [ ] 4. Smoke-test the self-built binary against an existing on-box GGUF — use
      `/models/Qwythos-9B-Claude-Mythos-5-1M-MTP-Q8_0.gguf` (9.1 GiB, smallest). Confirm
      coherent generation. **Do not proceed until this passes** — otherwise later failures
      are unattributable between "our build" and "our arch".
- [ ] 5. Install to `/opt/llamacpp-instella/` with the binary named
      **`llama-server-instella`** (see `add-instella-moe-skein-deployment` for why the
      rename is mandatory).

## Phase 1 — arch registration
- [ ] 6. `LLM_ARCH_INSTELLA_MOE` in `src/llama-arch.h`; `LLM_ARCH_NAMES` entry
      (`"instella-moe"`) in `src/llama-arch.cpp`.
- [ ] 7. Factory case + rope-type switch in `src/llama-model.cpp` — rope type must be
      `LLAMA_ROPE_TYPE_NORM` (consecutive-pair = interleaved), matching
      `rope_interleave: true`.
- [ ] 8. Entry in `src/llama-model-saver.cpp`.
- [ ] 9. Class declaration in `src/models/models.h`.
- [ ] 10. Entry in `tests/test-llama-archs.cpp` (new requirement, **undocumented** in
      HOWTO-add-model.md).

## Phase 2 — hparams + tensors
- [ ] 11. `load_arch_hparams`: read the MLA keys (`kv_lora_rank` 512, `q_lora_rank` 0,
      `key_length_mla` 96, `value_length_mla` 128), MoE keys (expert_count 64, used 6,
      shared 2, ff 1408, shared ff 2816, gating_func SIGMOID, weights_norm,
      weights_scale 2.5, group_count 1, group_used_count 1),
      `leading_dense_block_count` 1, and the YaRN fields.
- [ ] 12. `load_arch_tensors`: 484 tensors per §6 of the investigation. `attn_q` full-rank
      (no `attn_q_a`/`attn_q_b`). Require `attn_gate` — do **not** make it optional; its
      presence is what makes a mis-mapped deepseek2 load fail loudly.
- [ ] 13. Layer 0 dense (`ffn_{gate,up,down}`, ff 10944); layers 1–26 MoE.

## Phase 3 — the graph (the actual work)
- [ ] 14. Fork `src/models/deepseek2.cpp` → `src/models/instella-moe.cpp`.
- [ ] 15. Add the attention gate, copying the idiom from `src/models/afmoe.cpp`:
      `attn_out = ggml_mul(attn_out, ggml_sigmoid(attn_gate * attn_in))` applied **before**
      `o_proj`, where `attn_in` is the post-`attn_norm` input.
- [ ] 16. Verify `build_moe_ffn` can return the routed sum **without** the shared sum
      folded in. If it cannot, add that capability (likely a small signature change) —
      this is a prerequisite for step 17 and is assumed, not verified.
- [ ] 17. Thread **two** residual tensors:
      ```
      attn_in = attn_norm(inpL_nr);            // routed-free stream
      cur     = build_attn(attn_in);
      cur     = gated(cur, attn_in);           // step 15
      r       = ggml_add(inpL, cur);
      moe_in  = ffn_norm(inpL);                // PRE-attention → parallel block
      routed  = build_moe_ffn(moe_in);         // routed only
      shared  = build_ffn_shexp(moe_in);
      inpL    = ggml_add(ggml_add(r, routed), shared);
      inpL_nr = ggml_add(r, shared);
      ```
- [ ] 18. Layer 0: dense, `inpL_nr = inpL = embeddings`, still a parallel block.
      Layer 1: single-stream input. Layers 2–26: two-stream.
- [ ] 19. `output_norm` consumes **`inpL` only**; discard `inpL_nr`.
- [ ] 20. Optionally plumb `farskip_start_idx` / `farskip_end_idx` / `attn_only_farskip` /
      `mlp_only_farskip` as hparams. **Recommended** — not for Instella itself (which sets
      none of them) but because it lets the llama.cpp side reproduce the reference's B and
      C validation modes directly, making step 22 far sharper.

## Phase 4 — load and build gates
- [ ] 21. Load the F16 GGUF: zero unknown tensors, zero missing tensors,
      `n_created == n_tensors`.
- [ ] 22. Confirm all 27 layers report as offloaded to the GPU (`-ngl 99`); no CPU
      fallback for any operator. Capture the backend-assignment log as evidence.
- [ ] 23. `test-llama-archs` passes; `ggml` op-support probe shows no CPU fallback.
- [ ] 24. Note in the PR description that flash attention is unavailable at
      `K->ne[0] = 544` and that this is expected (see `add-fattn-mla-544-head-dim`).

**Correctness is deliberately NOT gated here** — that is
`add-instella-moe-correctness-harness`. "It loads and produces text" is explicitly not
acceptance for this change.
