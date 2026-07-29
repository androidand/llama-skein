# Tasks: Add Instella-MoE GGUF conversion

Target repo: **llama.cpp fork** (not llama-skein). Pin the upstream base commit and record
it here when the branch is cut.

Reference: `docs/investigations/instella-moe-w7800.md` §6 (full tensor mapping table) and
§3 (architecture facts). Template PR:
[#24260 cohere2-MoE](https://github.com/ggml-org/llama.cpp/pull/24260) (+632/−7, 13 files).

## Phase 1 — scaffold
- [ ] 1. Cut a branch from a pinned upstream commit; record base SHA in this file.
- [ ] 2. Add `MODEL_ARCH.INSTELLA_MOE` + tensor list to `gguf-py/gguf/constants.py`.
- [ ] 3. Confirm `gguf-py/gguf/tensor_mapping.py` needs **no** change —
      `model.layers.{bid}.self_attn.gate_proj` → `attn_gate` already exists at
      `tensor_mapping.py:385` (added for afmoe). Verify, don't assume.
- [ ] 4. Add `conversion/instella.py` with `InstellaMoEModel(DeepseekV2Model)`; register
      on `InstellaMoEForCausalLM` via `architectures`, **not** `model_type`.
- [ ] 5. Register the module in `conversion/__init__.py`.

## Phase 2 — tensor mapping
- [ ] 6. Global: `embed_tokens`→`token_embd`, `lm_head`→`output` (**not** tied),
      `model.norm`→`output_norm`.
- [ ] 7. Attention (27 layers): `q_proj`→`attn_q` (full-rank; `q_lora_rank` is null →
      emit `q_lora_rank = 0`), `kv_a_proj_with_mqa`→`attn_kv_a_mqa` [544,2048],
      `kv_a_layernorm`→`attn_kv_a_norm`, `kv_b_proj`→`attn_kv_b`, `o_proj`→`attn_output`.
- [ ] 8. Derive `attn_k_b` [512,96,16] and `attn_v_b` [512,128,16] by splitting
      `kv_b_proj` [3584,512] as 16 heads × (96 nope + 128 v) with `k_b.transpose(1,2)` —
      reuse the existing `conversion/deepseek.py` transform; head dims are 96/128 here
      vs DeepSeek-V3's 128/128.
- [ ] 9. Map `self_attn.gate_proj`→`attn_gate` [2048,2048].
- [ ] 10. Layer 0 only (dense, ff 10944): `mlp.{gate,up,down}_proj`→`ffn_{gate,up,down}`.
- [ ] 11. Layers 1–26 router: `mlp.gate.weight`→`ffn_gate_inp` (**keep F32**),
      `mlp.gate.e_score_correction_bias`→`exp_probs_b.bias` (already F32).
- [ ] 12. Stack the 64 experts per layer into 3-D `ffn_{gate,up,down}_exps` using the
      existing `_experts` accumulate-then-merge path.
- [ ] 13. Shared experts: straight copy to `ffn_{gate,up,down}_shexp` — already **fused**
      at ff 2816 = 1408 × 2, nothing to concatenate. Do **not** emit `ffn_gate_inp_shexp`
      (Instella's shared experts are unconditional).

## Phase 3 — metadata
- [ ] 14. Emit all standard keys per §6 of the investigation: block_count 27,
      context_length 32768, embedding_length 2048, feed_forward_length 10944,
      head_count 16, head_count_kv 16, kv_lora_rank 512, q_lora_rank 0,
      key_length 128, value_length 128, key_length_mla 96, value_length_mla 128,
      rope.dimension_count 32, rope.freq_base 8000000, expert_count 64,
      expert_used_count 6, expert_shared_count 2, expert_feed_forward_length 1408,
      expert_shared_feed_forward_length 2816, expert_weights_scale 2.5,
      expert_weights_norm true, expert_gating_func SIGMOID, expert_group_count 1,
      expert_group_used_count 1, leading_dense_block_count 1.
- [ ] 15. **YaRN**: emit `rope.scaling.{type=yarn,factor=40,original_context_length=4096}`
      and the `yarn_log_multiplier` that reproduces effective scale **0.16562688**
      (`mscale = 0.1*ln(40)+1 = 1.3688879`; scale = `128**-0.5 * mscale**2`). Assert the
      computed value in a unit test — this is the highest-risk single number.
- [ ] 16. Assert **zero new GGUF metadata keys** are introduced.
- [ ] 17. Register the tokenizer hash against `deepseek-v3`; add to
      `convert_hf_to_gguf_update.py`.

## Phase 4 — validation (no inference required)
- [ ] 18. Convert to **F16** GGUF. Assert exactly **484** tensors.
- [ ] 19. `gguf_dump` the result; diff every tensor name+shape against the table in §6 of
      the investigation. Zero unknown or missing tensors.
- [ ] 20. Assert `ffn_gate_inp` and `exp_probs_b` are F32 in the output.
- [ ] 21. Tokenizer parity: round-trip a few thousand strings against HF's tokenizer —
      exact ID match. Must include all 818 added tokens, the 800 `place_holder_no_N`
      reserved slots, CJK text, and the fullwidth-bar specials
      (`<|begin_of_sentence|>` etc.).
- [ ] 22. Chat-template parity: byte-compare HF `apply_chat_template` output against the
      GGUF template rendered with `--jinja`, including a system message (emitted **raw
      with no role marker** immediately after BOS) and a tool-call turn.
- [ ] 23. Confirm `add_bos_token = true`, `add_eos_token = false`, bos 0 / eos 1 / pad 2.
- [ ] 24. Add a converter unit test in the repo's existing convert-test style.
