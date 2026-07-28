# Running AMD Instella-MoE-16B-A3B-Think through llama-skein on HP Z4 + Radeon Pro W7800

**Investigation date:** 2026-07-29
**Investigator:** Claude Code session, non-interactive
**Target host:** z4 (Proxmox node), LXC 102 `llama-skein-z4`, AMD Radeon Pro W7800 48 GB, gfx1100
**Model revision inspected:** `amd/Instella-MoE-16B-A3B-Think` @ `e67a4a54d81b19692ec85ea1d1c777aa5c0bfd83`
**llama.cpp upstream inspected:** `ggml-org/llama.cpp` master @ `e9fa0781f1c2` (2026-07-28)
**Deployed binary inspected:** `/opt/llamacpp-rocm-gfx110X/llama-server`, upstream base `956973c` (2026-07-15)

> **No inference was run.** No model weights were downloaded. Every number in
> sections 8 and 10 that concerns throughput is labelled as an estimate. Sections 3, 4,
> 7 and 8's VRAM math are derived from directly fetched config/tensor metadata and from
> `strings` inspection of the deployed binary. Section 9 is a **plan**, not results.

---

## 1. Executive summary

Instella-MoE-16B-A3B-Think cannot run through llama-skein today, and no amount of
configuration will change that. llama.cpp has no Instella support of any kind — dense or
MoE — neither in the binary deployed on z4 nor on current upstream master. There is no
community GGUF, and the only two Instella GGUFs that exist on Hugging Face are for the
older dense 3B and are non-functional by their own author's admission.

However, the gap is far narrower than "custom MoE architecture" suggests. The model
declares `"model_type": "deepseek_v3"` and is genuinely a DeepSeek-V3 derivative. Of its
six unusual features, **five are already implemented upstream**: MLA with a null
`q_lora_rank` (the `is_lite` path — which, by coincidence, triggers on
`n_layer == 27`, exactly Instella's layer count), sigmoid `noaux_tc` routing with the
`e_score_correction_bias`, shared experts, interleaved RoPE, and the YaRN
`mscale_all_dim` softmax-scale correction. Even the **gated attention** is done —
`LLM_TENSOR_ATTN_GATE` exists, `tensor_mapping.py:385` already maps the exact HF tensor
name `model.layers.{bid}.self_attn.gate_proj`, and `src/models/afmoe.cpp` implements
`attn_out * sigmoid(gate)` before `o_proj` identically to Instella.

**Exactly one feature is genuinely missing: FarSkip.** I obtained its precise formulation
from the model's own `modeling_instella_moe.py` and cross-checked it against the paper
(arXiv:2511.11505). FarSkip carries **two** residual streams across each layer boundary
and runs attention and the MoE FFN as a parallel block. It requires one extra activation
tensor and **zero new ggml operators**, and llama.cpp already has two precedents for
multi-stream residuals: DeepSeek-V4 hyper-connections (`src/models/deepseek4.cpp`, `inpL`
as `[n_embd, hc_mult, n_tokens]`) and Gemma 3n AltUp (4 streams). FarSkip is a strict
special case of the former.

**Verdict: Category D (new model architecture), at its lower bound** — roughly 600–800
lines mirroring the merged cohere2-MoE PR (#24260: +632/−7 across 13 files), with no new
operators, no ROCm backend work, and **zero new GGUF metadata keys**.

**Two findings change the value calculus, and they point in opposite directions from the
engineering verdict:**

1. **The license is research-only.** `license_name: researchrail`, "RESEARCH-ONLY RAIL
   Model License". The stated goal — suitability as a Skein worker agent — is production
   use on real work, which is arguably outside that license. This is a blocker for the
   intended purpose, not a footnote.
2. **The VRAM story is excellent but the comparison is unflattering.** At Q8_0 with the
   full 32K context Instella needs ~17.8 GB versus the 42,971 MB the already-installed
   `qwen3.6-35b-a3b-q8-0` consumes — 41% of the footprint. But that Qwen has a
   comparable active-parameter count (~3B vs 2.8B), a far longer context window, a
   commercially usable license, and needs zero engineering. Instella's selling point is
   being *fully open* (data + code), not being *better*.

**Recommendation: implement now as an upstream capability investment; do not pursue as a
fleet worker model.** See §15.

---

## 2. Current compatibility verdict

| Question | Answer | Evidence |
|---|---|---|
| Does the deployed z4 binary support Instella? | **No** | `strings libllama.so \| grep -i instella` → empty; `grep -i farskip` → empty |
| Does current upstream master? | **No** | no `instella*` or `farskip*` in `src/models/` (200+ files); 0 hits for `instella` across `llama-arch.{h,cpp}`, `gguf-py/gguf/constants.py`, `tensor_mapping.py` |
| Was it ever attempted? | Once, for the dense 3B | [issue #12270](https://github.com/ggml-org/llama.cpp/issues/12270), opened 2025-03-08, auto-closed stale 2025-04-24, never implemented, no PR |
| Any FarSkip work upstream? | **No** | 0 results across llama.cpp issues + PRs, all states |
| Does a community GGUF exist? | **No** | 6 `amd/Instella-MoE-*` repos are safetensors-only; only 2 Instella GGUFs exist, both dense 3B, both non-functional |
| Will conversion fail cleanly or silently mismap? | **Fails cleanly** | registry keys off `config.json→architectures[0]` (`conversion/base.py:1109`), not `model_type`; `InstellaMoEForCausalLM` is absent from all 196 `@ModelBase.register` names |
| Could someone force it through as deepseek2? | Converts, then **fails loudly at load** | hand-editing `architectures` to `DeepseekV3ForCausalLM` converts (dims work out, even `LLM_TYPE_16B` from `case 27`), but `attn_gate` is emitted and never requested → `n_created != n_tensors` → "wrong number of tensors" (`llama-model-loader.cpp:1317`) |

That last row is worth dwelling on. The extra `attn_gate` tensor is an **accidental safety
net** — it makes the wrong path fail loudly instead of silently. If a converter dropped
that tensor, the model *would* load and produce plausible-but-wrong output: sequential
residual instead of FarSkip, no attention gate. The brief's instruction not to accept
"the model loads" as proof of compatibility is exactly right, and this is the concrete
mechanism by which it could have gone wrong.

**Category: D, lower bound.** Requires: new arch enum, new `src/models/` graph builder,
new `conversion/` class, a `tests/test-llama-archs.cpp` entry, docs. Does *not* require:
new ggml operators, ROCm/HIP backend changes, new GGUF metadata keys, or
`tensor_mapping.py` changes.

---

## 3. Model architecture analysis

### 3.1 config.json — load-bearing values

```
architectures: ["InstellaMoEForCausalLM"]     model_type: "deepseek_v3"   ← misleading
farskip: true          gated_attention: true          qk_layernorm: true
hidden_size 2048       num_hidden_layers 27           num_attention_heads 16
num_key_value_heads 16 intermediate_size 10944 (dense L0)  moe_intermediate_size 1408
n_routed_experts 64    num_experts_per_tok 6          n_shared_experts 2
first_k_dense_replace 1                               moe_layer_freq 1
scoring_func sigmoid   topk_method noaux_tc           norm_topk_prob true
n_group 1              topk_group 1                   ← group routing is a NO-OP
routed_scaling_factor 2.5    aux_loss_alpha 0.001     seq_aux true
q_lora_rank null       kv_lora_rank 512               ← Q full-rank, only KV latent
qk_head_dim 128        qk_nope_head_dim 96            qk_rope_head_dim 32
v_head_dim 128         vocab_size 128896              tie_word_embeddings false
hidden_act silu        rms_norm_eps 1e-6
max_position_embeddings 32768   rope_theta 8_000_000  rope_interleave true
rope_scaling: yarn, factor 40, beta_fast 32, beta_slow 1,
              mscale 1.0, mscale_all_dim 1.0, original_max_position_embeddings 4096
num_nextn_predict_layers 0      bos 0  eos 1  pad 2   torch_dtype bfloat16
NO quantization_config (pure BF16)
```

Absent keys default (per `configuration_instella_moe.py`) to `farskip_start_idx=0`,
`farskip_end_idx=1e4`, `attn_only_farskip=False`, `mlp_only_farskip=False`
⇒ **FarSkip is active on all 27 layers.**

Set in code, not JSON (`configuration_instella_moe.py:192-196`):
**`self.head_dim = qk_rope_head_dim = 32`**, deliberately pinned so YaRN operates on 32
dims rather than `hidden_size/num_heads = 128`.

### 3.2 Attention: MLA (KV-side only) with a sigmoid output gate

- **Q path is full-rank** — `q_proj [2048, 2048]` → 16 heads × 128, split
  `qk_nope_head_dim 96` + `qk_rope_head_dim 32`. No `q_a_proj`/`q_b_proj` because
  `q_lora_rank: null`. This is the DeepSeek-V2-Lite shape.
- **KV path is latent** — `kv_a_proj_with_mqa [544, 2048]` → 512 latent + 32 shared RoPE
  key; latent RMSNorm'd by `kv_a_layernorm [512]`; `kv_b_proj [3584, 512]` → 16 × (96
  nope + 128 v).
- RoPE on the 32 rope dims only, **interleaved** (`rope_interleave: true` →
  `apply_rotary_pos_emb_interleave`).
- **The gate** (`modeling_instella_moe.py:124-127`), class `MLAGatedAttention`:
  ```python
  if self.gated_attention:
      attn_output = attn_output * torch.sigmoid(self.gate_proj(hidden_states))
  attn_output = self.o_proj(attn_output)
  ```
  Per-head, per-channel elementwise sigmoid gate on the attention output, computed from
  the post-`input_layernorm` attention input, applied **before** `o_proj`.
  `gate_proj [2048, 2048]` = `hidden_size → num_heads * v_head_dim`.

### 3.3 The softmax-scale trap

`rope_scaling.mscale_all_dim = 1.0` is **truthy**, so per transformers v4.57.1
`modeling_deepseek_v3.py`:

```
mscale  = 0.1 * mscale_all_dim * ln(factor) + 1 = 0.1 * ln(40) + 1 = 1.3688879
scaling = qk_head_dim**-0.5 * mscale**2 = 0.08838835 * 1.87385 = 0.16562688
```

**1.874× the naive `1/sqrt(128)`.** Getting this wrong produces output that is fluent and
plausible but measurably wrong — precisely the failure mode to guard against.
Fortunately llama.cpp already has this: the `[TAG_DEEPSEEK2_YARN_LOG_MUL_FIX]` handling
from [PR #17945](https://github.com/ggml-org/llama.cpp/pull/17945). The converter must
feed it the right `mscale`/`mscale_all_dim`; the graph needs no new code.

### 3.4 FarSkip — exact rewiring

Two independent sources agree. From `modeling_instella_moe.py`
(`FarSkipDecoderLayer.forward` + `FarSkipMoE.forward`):

```python
if self.farskip:
    if not isinstance(hidden_states, tuple):      # first farskip layer
        residual = input_to_attn = input_to_mlp = hidden_states
    else:
        residual      = hidden_states[0]
        input_to_attn = hidden_states[1]
        input_to_mlp  = residual                  # binds BEFORE the rebind below
...
attn_output, _ = self.self_attn(hidden_states=self.input_layernorm(input_to_attn), ...)
residual = residual + attn_output                 # rebind
input_to_mlp = self.post_attention_layernorm(input_to_mlp)
mlp_output, mlp_shared_output = self.mlp(input_to_mlp)
residual_no_routed = residual + mlp_shared_output
residual           = residual + mlp_output        # mlp_output = routed + shared
hidden_states = (residual, residual_no_routed)
```

`FarSkipMoE.forward` returns `(routed + shared, shared)`.

Writing `x_k` for the full stream and `x̃_k` for the routed-free stream:

| | Standard DeepSeek-V3 | Instella FarSkip |
|---|---|---|
| attention input | `LN1(x_k)` | `LN1(x̃_k)` — routed-free stream |
| FFN input | `LN2(x_k + a_k)` — post-attention | `LN2(x_k)` — **pre**-attention |
| output | `x_k + a_k + m_k` | `x_{k+1} = x_k + a_k + r_k + s_k`<br>`x̃_{k+1} = x_k + a_k + s_k` |

so `x̃_{k+1} = x_{k+1} − r_k`. The paper renders the same thing as
`attn-in_k = o_{k−2} + attn-out_{k−1} + shared-exp-out_{k−1}` and `mlp-in_k = o_{k−1}`,
which is identical.

**Two stacked changes:** (A) a parallel-residual block — the MoE never sees this layer's
attention output; (B) the far-skip proper — attention's input omits the *previous* layer's
routed-expert output, the tensor requiring the all-to-all. The routed output is not lost,
only delayed by one layer.

**Boundary cases:** layer 0 is FarSkip *and* dense, receives a plain tensor, so
`residual = input_to_attn = input_to_mlp = embeddings` — still a parallel block, emits a
plain tensor. Layer 1 also receives a plain tensor. Layers 2–26 use the two-stream path.
The routed-free stream is **discarded** at the end; `model.norm` consumes the full stream
only (`InstellaMoEModel.forward:327-329`).

**Is it sequentially expressible?** Yes, definitively. The reference implementation *is* a
plain sequential loop with no threads or streams; the only inter-layer state is a 2-tuple
of `[n_tokens, hidden_size]` tensors. On one GPU there is no collective to overlap. In
AMD's own SGLang fork the overlap is gated behind `FARSKIP_OVERLAPPED_DECODER_LAYER`
(default `"0"`) and changes only *when* an async handle is waited on, never what is
computed. The paper agrees: *"without careful explicit implementation, the models will not
automatically overlap communication with computation."*

**FarSkip is a performance idea whose architectural residue we must reproduce exactly to
get correct numbers, while gaining none of its speed benefit on a single GPU.**

**Not convertible back to a standard MoE.** The paper is explicit that connectivity is
genuinely changed — converting existing models to FarSkip required self-distillation.
There is no "equivalent to standard architecture" claim anywhere.

### 3.5 MoE routing

Upstream `DeepseekV3TopkRouter` verbatim; only `DeepseekV3MoE.forward` is overridden.

- **sigmoid** scoring, router logits computed in **fp32**.
- **`noaux_tc`** with a learned per-layer `e_score_correction_bias` (F32, 64 values) added
  to scores **for selection only** — gathered weights come from the *un-biased* scores.
- **Group-limited routing is a no-op**: `n_group = 1`, `topk_group = 1` (contrast
  DeepSeek-V3's 8/4). `score_mask` is all-ones.
- `norm_topk_prob: true` → top-6 weights divided by their sum (+1e-20), **then** scaled
  by `routed_scaling_factor = 2.5`.
- 64 routed, top-6; **2 shared experts stored fused** as one ff-2816 MLP.
- MoE on layers 1–26; **layer 0 dense** (ff 10944). Verified per-layer from the
  safetensors index, not merely from `first_k_dense_replace`.
- Expert MLP is standard SwiGLU. `aux_loss_alpha`/`seq_aux` are training-only.

### 3.6 Tensor inventory (5,344 tensors, shapes read from safetensors headers)

| count | pattern | shape |
|---|---|---|
| 1 | `model.embed_tokens.weight` | [128896, 2048] |
| 1 | `lm_head.weight` | [128896, 2048] |
| 1 | `model.norm.weight` | [2048] |
| 27 | `…input_layernorm.weight` | [2048] |
| 27 | `…post_attention_layernorm.weight` | [2048] |
| 27 | `…self_attn.q_proj.weight` | [2048, 2048] |
| 27 | `…self_attn.kv_a_proj_with_mqa.weight` | [544, 2048] |
| 27 | `…self_attn.kv_a_layernorm.weight` | [512] |
| 27 | `…self_attn.kv_b_proj.weight` | [3584, 512] |
| 27 | `…self_attn.gate_proj.weight` | [2048, 2048] |
| 27 | `…self_attn.o_proj.weight` | [2048, 2048] |
| 1 | `model.layers.0.mlp.{gate,up}_proj.weight` | [10944, 2048] |
| 1 | `model.layers.0.mlp.down_proj.weight` | [2048, 10944] |
| 26 | `…mlp.gate.weight` | [64, 2048] |
| 26 | `…mlp.gate.e_score_correction_bias` | [64] **F32** |
| 1664 | `…mlp.experts.{e}.{gate,up}_proj.weight` | [1408, 2048] |
| 1664 | `…mlp.experts.{e}.down_proj.weight` | [2048, 1408] |
| 26 | `…mlp.shared_experts.{gate,up}_proj.weight` | [2816, 2048] |
| 26 | `…mlp.shared_experts.down_proj.weight` | [2048, 2816] |

Shape arithmetic checks out: `kv_a_proj_with_mqa` = 512+32 = 544;
`kv_b_proj` = 16×(96+128) = 3584; `self_attn.gate_proj` = 16×128 = 2048;
`shared_experts` ff 2816 = 1408 × 2 (fused).

**Absent:** `q_a_proj`/`q_b_proj`/`q_a_layernorm`, `rotary_emb.inv_freq`, MTP layers, any
bias except the F32 router bias. Embeddings **not** tied.

### 3.7 Parameter accounting — reproduces the official byte count exactly

```
embed + lm_head        527,958,016     norms                126,464
attn (27 × 15,532,032) 419,364,864     dense L0          67,239,936
routed experts      14,394,851,328     shared experts   449,839,104
router                   3,407,872     router bias            1,664
TOTAL               15,862,789,248
BF16 bytes = 15,862,789,248×2 + 1,664×2 = 31,725,581,824  ==  index total_size  ✓
```

Active per token ≈ 2.29 B excluding embed + lm_head, **≈ 2.82 B including them** — which
is how AMD arrives at "2.8B active".

### 3.8 Tokenizer

- **Byte-level BPE** — `model.type BPE`, 128,000 base entries, 127,741 merges,
  `byte_fallback: false`, `unk: null`, empty normalizer. `tokenizer_class` claims
  `LlamaTokenizerFast` but the artifact is **DeepSeek's tokenizer, not SentencePiece**.
- `vocab_size` 128896 vs max added id 128814 → ~81 padded rows.
- 818 added tokens: ids 0/1/2 = begin/end-of-sentence/pad (DeepSeek fullwidth-bar forms);
  128000–128799 = 800 reserved `place_holder_no_N`; 128800–128814 = 3 FIM, `User`,
  `Assistant`, `EOT`, and 8 tool-call/tool-output markers.
- ⚠️ `User`/`Assistant`/tool tokens are `special: false, normalized: true` — a known
  DeepSeek quirk that trips converters.
- **The pre-tokenizer is byte-identical to `LLAMA_VOCAB_PRE_TYPE_DEEPSEEK3_LLM`**
  (`src/llama-vocab.cpp:318-325`): three matching Split regexes then
  `ByteLevel(add_prefix_space=false, use_regex=false)`. ⇒ conversion needs only the
  tokenizer **hash registered against `deepseek-v3``. Zero regex work.
- Chat template = **DeepSeek-R1's verbatim**. System prompt emitted **raw with no role
  marker** immediately after BOS. `add_bos_token: true`, `add_eos_token: false`.
- **No `<think>`/`</think>` tokens exist** — checked both `added_tokens` and the base
  vocab. The template contains no reasoning delimiter and does not pre-open a think block;
  it ends `{{'<|Assistant|>'}}`. No AMD documentation specifies a reasoning format for the
  `-Think` checkpoint.
  ⚠️ **Whether it emits a delimiter at all is UNVERIFIED** and must be determined
  empirically from the reference run. Do not assume "there is none".

### 3.9 Context length — four disagreeing numbers

| source | value |
|---|---|
| Think `max_position_embeddings` | **32768** |
| Base `max_position_embeddings` | 65536 |
| `tokenizer_config.model_max_length` | 131072 |
| ROCm blog / GitHub README | "extended to 64K" |
| YaRN 4096 × 40 | 163840 |

**Use 32768 for Think.** The base model was long-context-trained to 64K; the SFT/DPO/RL
checkpoints declare 32768 and AMD evaluated generation up to 32768. The YaRN factor of 40
is the carried-forward extension recipe, not a usable-window claim — and it is what
perturbs the softmax scale in §3.3.

All six sibling checkpoints share identical architecture (27L, 2048h, 64+2 experts, top-6,
farskip, gated_attention, vocab 128896, `first_k_dense_replace: 1`); only ctx/theta/MTP
differ — Pretrain/Midtrain 4096 / θ=1e4 / MTP 1; Base 65536 / θ=8e6 / MTP 1;
SFT, DPO, Think 32768 / θ=8e6 / MTP 0.

### 3.10 License — research only ⛔

`license: other`, `license_name: researchrail`. The `LICENSE` file is titled
**"RESEARCH-ONLY RAIL Model License"**. Model card: "licensed for academic and research
purposes under a ResearchRAIL license", "being released for research purposes only … not
intended for use cases requiring high levels of factual accuracy, safety-critical
applications, or health and medical applications."

**Not usable for commercial deployment.** Directly relevant to the brief's stated goal of
evaluating suitability as a Skein worker agent.

---

## 4. llama.cpp feature comparison

Against master @ `e9fa0781f1c2`. 139 architectures in `src/llama-arch.h`; the binary
deployed on z4 has 114.

| Instella feature | llama.cpp status | Where |
|---|---|---|
| MLA, `q_lora_rank = null` | ✅ `is_lite` path — and `is_lite` triggers on `n_layer()==27`, exactly Instella's count | `src/models/deepseek2.cpp:8` |
| Native compressed-KV MLA with absorption | ✅ mature; `wk_b` absorbs `q_nope`, `wv_b` as `v_mla`; decompress-to-MHA exists only as legacy fallback | `deepseek2.cpp:290-328` (fallback `:329-366`) |
| MLA GGUF keys | ✅ `attention.{q_lora_rank,kv_lora_rank,key_length_mla,value_length_mla}` | `llama-arch.cpp:239,240,253,254` |
| `attn_k_b` / `attn_v_b` split | ✅ done **at conversion** with `k_b.transpose(1,2)` | `conversion/deepseek.py` |
| sigmoid routing + `noaux_tc` bias | ✅ `expert_gating_func` + `ffn_exp_probs_b`, used by 22 arches | `llm_graph_context::build_moe_ffn` |
| shared experts | ✅ `ffn_*_shexp`, 33 arches | ditto |
| `expert_weights_norm` / `_scale` | ✅ | ditto |
| group-limited routing | ✅ generic, gated on `n_expert_groups > 1`; Instella's 1/1 makes it a no-op | ditto |
| fused 3-D expert tensors | ✅ `ffn_{gate,up,down}_exps`, converter stacks with `merge_expert = True` | ditto |
| **gated attention** | ✅ `LLM_TENSOR_ATTN_GATE`; **`tensor_mapping.py:385` already maps `model.layers.{bid}.self_attn.gate_proj`**; `afmoe.cpp` implements `attn_out * sigmoid(gate)` before `o_proj` identically. Also in `qwen35`, `qwen35moe`, `qwen3next`, `step35`, `gemma3n` | `src/models/afmoe.cpp` |
| `rope_interleave: true` | ✅ `LLM_ARCH_DEEPSEEK2` → `LLAMA_ROPE_TYPE_NORM` = consecutive-pair = interleaved. **Matches.** | `llama-model.cpp` rope-type switch |
| YaRN + `mscale_all_dim` | ✅ `[TAG_DEEPSEEK2_YARN_LOG_MUL_FIX]` | [PR #17945](https://github.com/ggml-org/llama.cpp/pull/17945) |
| DeepSeek-V3 pre-tokenizer | ✅ byte-identical regexes | `llama-vocab.cpp:318-325` |
| `GGML_OP_MUL_MAT_ID` on gfx1100 | ✅ no arch gating in CUDA/HIP `supports_op` | — |
| **FarSkip two-stream residual** | ❌ **the only real gap** | — |

### Multi-stream residuals: two precedents already in master

Standard builders thread a single `inpL` (`deepseek2.cpp:422`: `inpL = cur;`). But:

- **DeepSeek-V4 hyper-connections** — `src/models/deepseek4.cpp` carries `inpL` as
  `[n_embd, hparams.dsv4_hc_mult, n_tokens]`: *hc* parallel residual streams with a
  learned src→dst mixing matrix, via `build_hc_pre` / `build_hc_post`, backed by
  first-class fused ops `GGML_OP_DSV4_HC_{PRE,POST,COMB}` with CUDA kernels.
- **Gemma 3n AltUp** — `src/models/gemma3n.cpp` makes `inpL` `[n_embd, n_tokens, n_altup]`
  with 4 streams and `altup_predict`/`altup_correct` per layer.

**FarSkip is a strict special case of the first: 2 streams with fixed routing** and no
learned mixing matrix. It therefore needs no new memory or scheduling machinery — just
`inpL` as a 2-slice tensor (or simply two separate `ggml_tensor*`), plus optional
`farskip_start_idx` / `farskip_end_idx` / `attn_only_farskip` / `mlp_only_farskip` hparams.

---

## 5. Missing functionality

Ranked by risk, not by lines of code.

| # | Gap | Risk | Notes |
|---|---|---|---|
| 1 | **FarSkip two-stream residual + parallel block** | **High** — silent numerical error if wrong | The only genuinely new graph logic. Two extra `ggml_add`s and one extra live tensor. Needs `build_moe_ffn` to expose the routed sum *without* the shared sum folded in — verify, don't assume |
| 2 | **Arch registration** (`LLM_ARCH_INSTELLA_MOE`, enum, name, factory, rope-type switch) | Low | mechanical |
| 3 | **Converter class** keyed on `architectures`, **not** `model_type` (which lies: `deepseek_v3`) | Medium | thin `DeepseekV2Model` subclass; must not be registered on `model_type` |
| 4 | **YaRN mscale plumbed correctly** so the effective scale is 0.16562688 | **High** — plausible-but-wrong output | existing code, but must be fed the right values |
| 5 | Tokenizer hash registered against `deepseek-v3` | Low | hash only, no regex |
| 6 | `tests/test-llama-archs.cpp` entry | Low | new requirement, **undocumented** in HOWTO |
| 7 | **Flash-attention kernel at head_dim 544** | Medium (perf only) | see §7 |

**Not needed:** new ggml operators; ROCm/HIP backend changes; new GGUF metadata keys;
`tensor_mapping.py` changes; CMake changes (`src/CMakeLists.txt:9` globs `models/*.cpp`).

---

## 6. Proposed GGUF tensor mapping

Target arch: `instella-moe` / `LLM_ARCH_INSTELLA_MOE`. Base template: `deepseek2`.

**Confidence:** HIGH = shape-verified *and* an identical transform already exists for
deepseek2; MED = mechanically clear but new; LOW = needs empirical confirmation.

### Global

| HF name | GGUF name | Shape | Transform | Conf |
|---|---|---|---|---|
| `model.embed_tokens.weight` | `token_embd.weight` | [128896, 2048] | none | HIGH |
| `lm_head.weight` | `output.weight` | [128896, 2048] | none (**not** tied) | HIGH |
| `model.norm.weight` | `output_norm.weight` | [2048] | none | HIGH |

### Per-layer attention (all 27)

| HF name | GGUF name | Shape | Transform | Conf |
|---|---|---|---|---|
| `input_layernorm.weight` | `blk.N.attn_norm.weight` | [2048] | none | HIGH |
| `self_attn.q_proj.weight` | `blk.N.attn_q.weight` | [2048, 2048] | none — `q_lora_rank==0` / `is_lite` path | HIGH |
| `self_attn.kv_a_proj_with_mqa.weight` | `blk.N.attn_kv_a_mqa.weight` | [544, 2048] | none | HIGH |
| `self_attn.kv_a_layernorm.weight` | `blk.N.attn_kv_a_norm.weight` | [512] | none | HIGH |
| `self_attn.kv_b_proj.weight` | `blk.N.attn_kv_b.weight` | [3584, 512] | none (legacy path) | HIGH |
| ↳ derived | `blk.N.attn_k_b.weight` | [512, 96, 16] | reshape [16,224,512], rows `[:, :96, :]`, `transpose(1,2)` | HIGH |
| ↳ derived | `blk.N.attn_v_b.weight` | [512, 128, 16] | reshape [16,224,512], rows `[:, 96:, :]` | HIGH |
| `self_attn.o_proj.weight` | `blk.N.attn_output.weight` | [2048, 2048] | none | HIGH |
| **`self_attn.gate_proj.weight`** | **`blk.N.attn_gate.weight`** | [2048, 2048] | none — **mapping already exists** (`tensor_mapping.py:385`) | HIGH |

`kv_b_proj` rows are 16 heads × (96 nope + 128 v) = 3584. This is exactly the existing
deepseek2 split, only with head dims 96/128 instead of DeepSeek-V3's 128/128.

### Layer 0 only (dense)

| HF name | GGUF name | Shape | Conf |
|---|---|---|---|
| `post_attention_layernorm.weight` | `blk.0.ffn_norm.weight` | [2048] | HIGH |
| `mlp.gate_proj.weight` | `blk.0.ffn_gate.weight` | [10944, 2048] | HIGH |
| `mlp.up_proj.weight` | `blk.0.ffn_up.weight` | [10944, 2048] | HIGH |
| `mlp.down_proj.weight` | `blk.0.ffn_down.weight` | [2048, 10944] | HIGH |

### Layers 1–26 (MoE)

| HF name | GGUF name | Shape | Transform | Conf |
|---|---|---|---|---|
| `post_attention_layernorm.weight` | `blk.N.ffn_norm.weight` | [2048] | none | HIGH |
| `mlp.gate.weight` | `blk.N.ffn_gate_inp.weight` | [64, 2048] | none; **keep F32** | HIGH |
| `mlp.gate.e_score_correction_bias` | `blk.N.exp_probs_b.bias` | [64] | none; already F32 | HIGH |
| `mlp.experts.{0..63}.gate_proj.weight` | `blk.N.ffn_gate_exps.weight` | [2048, 1408, 64] | **stack 64 → 3-D** | HIGH |
| `mlp.experts.{0..63}.up_proj.weight` | `blk.N.ffn_up_exps.weight` | [2048, 1408, 64] | **stack 64 → 3-D** | HIGH |
| `mlp.experts.{0..63}.down_proj.weight` | `blk.N.ffn_down_exps.weight` | [1408, 2048, 64] | **stack 64 → 3-D** | HIGH |
| `mlp.shared_experts.gate_proj.weight` | `blk.N.ffn_gate_shexp.weight` | [2816, 2048] | none — **already fused** | HIGH |
| `mlp.shared_experts.up_proj.weight` | `blk.N.ffn_up_shexp.weight` | [2816, 2048] | none | HIGH |
| `mlp.shared_experts.down_proj.weight` | `blk.N.ffn_down_shexp.weight` | [2048, 2816] | none | HIGH |

Expert stacking uses the existing `_experts` accumulate-then-merge path. Unlike
DeepSeek-V3 there is **nothing to concatenate** for the shared experts — they are stored
pre-fused at ff 2816, so it is a straight copy. Do **not** emit `ffn_gate_inp_shexp`;
Instella's shared experts are unconditional.

**GGUF tensor count:** 3 global + 27×9 attn + 4 (L0 dense) + 26×9 (MoE) = **484**, down
from 5,344 HF tensors — the 4,992 expert tensors collapse into 78 3-D tensors.

### Metadata keys

All existing. Values from config.json:

```
general.architecture                            = "instella-moe"
instella-moe.block_count                        = 27
instella-moe.context_length                     = 32768
instella-moe.embedding_length                   = 2048
instella-moe.feed_forward_length                = 10944   (dense L0)
instella-moe.attention.head_count               = 16
instella-moe.attention.head_count_kv            = 16
instella-moe.attention.layer_norm_rms_epsilon   = 1e-6
instella-moe.attention.q_lora_rank              = 0       (null → full-rank q_proj)
instella-moe.attention.kv_lora_rank             = 512
instella-moe.attention.key_length               = 128     (qk_head_dim)
instella-moe.attention.value_length             = 128     (v_head_dim)
instella-moe.attention.key_length_mla           = 96      (qk_nope_head_dim)
instella-moe.attention.value_length_mla         = 128
instella-moe.rope.dimension_count               = 32      (qk_rope_head_dim)
instella-moe.rope.freq_base                     = 8000000
instella-moe.rope.scaling.type                  = "yarn"
instella-moe.rope.scaling.factor                = 40
instella-moe.rope.scaling.original_context_length = 4096
instella-moe.rope.scaling.yarn_log_multiplier   = <mscale-derived — see §3.3>
instella-moe.expert_count                       = 64
instella-moe.expert_used_count                  = 6
instella-moe.expert_shared_count                = 2
instella-moe.expert_feed_forward_length         = 1408
instella-moe.expert_shared_feed_forward_length  = 2816
instella-moe.expert_weights_scale               = 2.5
instella-moe.expert_weights_norm                = true
instella-moe.expert_gating_func                 = 2       (SIGMOID)
instella-moe.expert_group_count                 = 1
instella-moe.expert_group_used_count            = 1
instella-moe.leading_dense_block_count          = 1
tokenizer.ggml.pre                              = "deepseek-v3"   (hash registration)
tokenizer.ggml.add_bos_token = true / add_eos_token = false
tokenizer.ggml.bos_token_id = 0 / eos = 1 / padding = 2
```

**Zero new GGUF metadata keys.** `farskip` and `gated_attention` should be **implied by
the architecture** rather than stored — all six released checkpoints set both to true, and
a checkpoint with `farskip: false` would simply *be* `deepseek2` and should convert as
such. If AMD later ships a non-FarSkip Instella-MoE, add a key then. This keeps the
upstream surface minimal.

### Files to change

The layout has moved — **`docs/development/HOWTO-add-model.md` is stale**; it still says
graphs live in `src/llama-model.cpp` and never mentions `src/models/` or
`tests/test-llama-archs.cpp`.

| File | Change | Est. |
|---|---|---|
| `conversion/instella.py` | new `InstellaMoEModel(DeepseekV2Model)`, registered on `InstellaMoEForCausalLM` | ~100 |
| `conversion/__init__.py` | register module | 1 |
| `gguf-py/gguf/constants.py` | `MODEL_ARCH.INSTELLA_MOE` + tensor list | ~30 |
| `src/llama-arch.h` / `.cpp` | enum + `LLM_ARCH_NAMES` entry | 2 |
| `src/models/instella-moe.cpp` | **NEW** — the actual work | ~350–450 |
| `src/models/models.h` | class declaration | ~17 |
| `src/llama-model.cpp` | factory + rope-type switch | ~8 |
| `src/llama-model-saver.cpp` | entry | 1 |
| `tests/test-llama-archs.cpp` | entry | ~2 |
| `gguf-py/gguf/tensor_mapping.py` | **none needed** | 0 |
| `src/CMakeLists.txt` | **none needed** (globs `models/*.cpp`) | 0 |

Concrete template: **[PR #24260 "Add arch support for cohere2-MoE"](https://github.com/ggml-org/llama.cpp/pull/24260)**,
merged 2026-06-13, 21 commits, **+632 / −7 across 13 files**, of which
`src/models/cohere2moe.cpp` was +443. Instella-MoE should land in the same envelope.

### The only genuinely novel graph logic

```
// inpL    = full stream        x_k
// inpL_nr = routed-free stream x~_k
attn_in  = attn_norm(inpL_nr);                        // ← routed-free
cur      = build_attn(attn_in);
cur      = ggml_mul(cur, ggml_sigmoid(attn_gate * attn_in));   // gated MLA, cf. afmoe.cpp
r        = ggml_add(inpL, cur);
moe_in   = ffn_norm(inpL);                            // ← PRE-attention: parallel block
routed   = build_moe_ffn(moe_in);                     // routed sum ONLY
shared   = build_ffn_shexp(moe_in);
inpL     = ggml_add(ggml_add(r, routed), shared);     // x_{k+1}
inpL_nr  = ggml_add(r, shared);                       // x~_{k+1}
```

Layer 0: dense, `inpL_nr = inpL = embeddings`, still a parallel block. Layer 1 also
single-stream. Layers 2–26 two-stream. `output_norm` consumes **`inpL` only** — discard
`inpL_nr`.

### A staged-validation lever

`configuration_instella_moe.py` exposes flags that **decompose the two changes**:

- `mlp_only_farskip=True` → pure parallel residual, no far-skip
- `attn_only_farskip=True` → sequential MLP, far-skip only

Instella sets neither, but **the reference implementation can be run in each mode** to
produce three independent logit oracles. That isolates "did I get the parallel block
right?" from "did I get the two-stream right?" — far better than one all-or-nothing
comparison. This is the single most useful debugging affordance found in this
investigation.

---

## 7. ROCm compatibility analysis

### Verified environment (z4, LXC 102)

| Item | Value |
|---|---|
| GPU | Radeon Pro W7800 48 GB, **gfx1100** (RDNA3), 70 CU, PCI `[1002:7449]` |
| VRAM | 51,522,830,336 B = **49,136 MiB**; 45 MiB used idle |
| amdgpu | ip block `gfx_v11_0_0`; `/dev/dri` + `/dev/kfd` bind-mounted into the LXC |
| ROCm **in-container** | **7.2.4** at `/opt/rocm`; `hipcc` HIP 7.2.53211, AMD clang 22.0.0git |
| Build toolchain | `cmake`, `git`, `gcc`, `g++`, `make`, `python3` 3.12.3, `pip3` — **all present** |
| Missing | `ninja`, `wget`, `huggingface_hub`, `torch`, `transformers`, `safetensors`, `gguf` |
| Container | Ubuntu 24.04.4, 8 cores, **48 GiB RAM**, rootfs 250 G / **100 G free** |
| Host | 125 GiB RAM, `rpool` 944 G with **779 G free** |

**We can build llama.cpp for gfx1100 in place.** That was the largest open risk and it is
resolved.

### Operator coverage

- `GGML_OP_MUL_MAT_ID` (the MoE expert matmul) has **no arch gating** in the CUDA/HIP
  `supports_op` — works on gfx1100.
- FarSkip needs only `ggml_add`; the attention gate needs only `ggml_sigmoid` + `ggml_mul`.
  **No operator will fall back to CPU** on account of this architecture.
- `AMD_WMMA_AVAILABLE` / `amd_wmma_available(cc)` = RDNA3 ∪ RDNA4
  (`common.cuh:269,340`). `amd_mfma_available` is CDNA-only, so irrelevant here.

### 🔴 Flash attention will be unavailable — and this is a new finding

The RDNA3 large-head-dim instability that caused z4's historical wedge is now handled
upstream by a **hard cap**: in `ggml_cuda_get_best_fattn_kernel` (`fattn.cu`), WMMA /
`MMA_F16` is selected on RDNA3 only when `Q->ne[0] <= 128`; above that it falls to
`BEST_FATTN_KERNEL_TILE`. So the WMMA path is no longer the hazard it was.

But for Instella specifically, with MLA absorption:

```
K->ne[0] = kv_lora_rank + qk_rope_head_dim = 512 + 32 = 544
V->ne[0] = 512
```

The `switch (K->ne[0])` instantiates only `{40, 64, 72, 80, 96, 112, 128, 192, 256, 320,
512, 576}`. DeepSeek's **576** (512 + 64) is present. **544 is not** → `default: return
BEST_FATTN_KERNEL_NONE`.

Consequence: `-fa auto` (the default, `common/arg.cpp:1666`) probes `supports_op` and
**silently disables flash attention**. Output remains correct; long-context prompt
processing is slower. The fix is a one-line `<544, 512>` kernel instantiation — a
genuinely small, independently useful upstream contribution, since the 544 dimension
follows from `qk_rope_head_dim = 32` and will recur in any model using that.

**Interaction with llama-skein:** z4's config sets `tuning: flash_attn: true`, and
`internal/tuning/inject.go:131-165` injects flags into **every** model whose `backend` is
`""` or `llamacpp`, with **no per-model opt-out**. Explicit flags in `cmd` win
(`apply.go:39-48`), so pin the desired value there.

### KV cache

MLA keeps a single K-only latent cache of `544 × 2 B × 27 layers` = **28.7 KiB/token**
(~918 MiB at 32K). This is 7.5× smaller than the decompressed-MHA layout the HF reference
implementation uses (216 KiB/token, 6.75 GiB at 32K), because
`modeling_instella_moe.py` calls `past_key_values.update(...)` with *decompressed* per-head
K/V. **Any VRAM claim must state which layout it assumes.**

### Open ROCm/gfx1100 issues worth tracking

| Issue | Relevance |
|---|---|
| [#24906 ROCm reports incorrect free VRAM](https://github.com/ggml-org/llama.cpp/issues/24906) | **Directly affects llama-skein's fit engine on gfx1100**, independent of Instella — `fit.go:334-361` builds its budget from `VRAMFreeMB` |
| [#19482 large model loading hangs on ROCm](https://github.com/ggml-org/llama.cpp/issues/19482) | 16 GB+ loads |
| [#14727 nondeterministic output at temp 0](https://github.com/ggml-org/llama.cpp/issues/14727) | **Undermines greedy-match correctness testing** — see §12 |
| [#25620 MMVQ nwarps mismatch → NaN](https://github.com/ggml-org/llama.cpp/issues/25620) | gfx11-generic / RDNA3.5; gfx1100 has a specific target so likely unaffected |
| [#26027 GLM-5.2 dense-MLA CUDA path subtly corrupted](https://github.com/ggml-org/llama.cpp/issues/26027) | open, 2026-07-23 — the only open MLA-titled issue; MLA is not bug-free |
| [PR #24546 size routed-MoE MMQ N-tiles from typical expert width on RDNA3](https://github.com/ggml-org/llama.cpp/pull/24546) | MoE-on-RDNA3 tuning; Instella's ff 1408 is narrow, so likely relevant |

### Build command for gfx1100

```bash
HIPCXX="$(hipconfig -l)/clang" HIP_PATH="$(hipconfig -R)" \
  cmake -S . -B build -DGGML_HIP=ON -DGPU_TARGETS=gfx1100 \
        -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=ON \
        -DLLAMA_SERVER_SSL=OFF -DLLAMA_BUILD_UI=OFF -DLLAMA_BUILD_WEBUI=OFF \
  && cmake --build build --config Release -t llama-server -j 8
```

(Flags cross-checked against llama-skein's own `sourceCmakeArgs`,
`internal/runtime/llamacpp_upgrade.go:564-585`, which uses `-DAMDGPU_TARGETS` and
`-DCMAKE_HIP_COMPILER=<rocm>/bin/amdclang++`.)

### Performance bottlenecks

- **PCIe 3.0** — irrelevant if all layers stay on GPU, which they will at every
  quantization (§8). Only matters on cold load: ~16 GB at Q8 over PCIe 3.0 x16
  (~12 GB/s theoretical) plus ZFS read.
- **CPU offload** — not needed. Avoid it: llama-skein's fit engine does not model MoE
  total-vs-active and charges CPU-offloaded experts against VRAM anyway (§11 risk R4).
- **Expert routing** — 64 experts at ff 1408 is a *narrow* expert. Narrow experts stress
  the MoE matmul's N-tiling; see PR #24546.
- **Missing FA at 544** — the one real, identified penalty, at long context.
- **Container RAM 48 GiB** — enough for conversion (streams tensor-by-tensor) but
  it is half the host's 125 GiB. Worth knowing before assuming headroom.

---

## 8. VRAM and quantization estimates

### File sizes

Quantizable params = 15,859,253,248 (total minus norms, `ffn_gate_inp`, and
`exp_probs_b`, which stay F32 = 13 MiB).

| quant | bpw | GB | GiB | MiB |
|---|---|---|---|---|
| BF16 / F16 | 16.0 | 31.73 | 29.55 | 30,263 |
| **Q8_0** | 8.5 | 16.86 | 15.71 | **16,083** |
| Q6_K | 6.5625 | 13.02 | 12.13 | 12,420 |
| Q5_K_M | ~5.7 | 11.31 | 10.54 | 10,790 |
| Q4_K_M | ~4.85 | 9.63 | 8.97 | 9,183 |
| IQ4_XS | ~4.25 | 8.44 | 7.86 | 8,048 |

Method calibrated against the on-box baseline: `Qwen3.6-35B-A3B-Q8_0.gguf` is
37,801,097,504 B for ~35 B params = 1.08 B/param, versus this method's 1.0625 + F32
overhead. Consistent. Per the brief's caution, this is **not** naive
`params × bpw`: norms, the router, and the router bias are held at F32.

### KV cache per context

| layout | B/token | 4K | 8K | 16K | 32K |
|---|---|---|---|---|---|
| **latent MLA** (llama.cpp) | 29,376 (28.7 KiB) | 115 MiB | 230 MiB | 459 MiB | **918 MiB** |
| decompressed MHA (HF ref) | 221,184 (216 KiB) | 0.84 GiB | 1.69 GiB | 3.38 GiB | 6.75 GiB |

### Total VRAM, via llama-skein's own fit formula

`usable = 49,136 × 0.92 = 45,205 MiB` (`vramSafetyFrac`); model charged at ×1.05
(`computeOverheadFrac`); `internal/fit/fit.go:334-361`.

| quant | weights | KV @32K | required @32K | headroom | max ctx (latent) |
|---|---|---|---|---|---|
| BF16/F16 | 30,263 | 918 | **32,694** | 12,511 | 479,347 |
| **Q8_0** | 16,083 | 918 | **17,805** | 27,400 | 1,010,810 |
| Q6_K | 12,420 | 918 | 13,959 | 31,246 | 1,148,098 |
| Q5_K_M | 10,790 | 918 | 12,248 | 32,958 | 1,209,190 |
| Q4_K_M | 9,183 | 918 | 10,560 | 34,645 | 1,269,420 |
| IQ4_XS | 8,048 | 918 | 9,368 | 35,837 | 1,311,959 |

Runtime/ROCm overhead beyond the ×1.05 compute allowance is not separately modelled by
llama-skein; empirically the fit engine's estimate for the 35B baseline (42,971 MiB) sits
comfortably inside the card, so the ×1.05 plus the 8% safety margin is absorbing it.
Budget ~500–1000 MiB of additional ROCm context on top for planning purposes.

### Two conclusions that settle open questions in the brief

**1. Q8_0 is the right first quantization, and the Q8-vs-Q6 tradeoff does not exist.**
The brief proposed Q8_0 for quality *or* Q6_K for context headroom, and asked that this be
validated rather than assumed. Validated: **it is a false choice.** At Q8_0 with the full
32K window Instella needs 17,805 MiB — 41% of the baseline's 42,971 MiB — and the
theoretical max context is ~1.01 M tokens, roughly **31× the model's own 32,768 ceiling**.
Context is never the binding constraint. Q6_K buys headroom that cannot be spent and
costs quality. Go straight to Q8_0.

**2. F16 GGUF fits with the full 32K context** — 32,694 of 45,205 usable MiB. This is more
than a curiosity: it means the GGUF can be validated against the BF16 reference **with
zero quantization confound**. Convert to F16, prove the graph is right, *then* quantize
and measure the quantization delta separately. That is a materially stronger test path
than validating Q8_0 directly against BF16 and trying to attribute any discrepancy.

Even the pessimistic decompressed-MHA layout is comfortable: Q8_0 + 32K =
16,083 × 1.05 + 6,912 = 23,799 MiB.

### Disk budget — the real constraint

`/models` has **106,595,745,792 B = 99.3 GiB** free. A full run needs approximately:

| item | GiB |
|---|---|
| BF16 safetensors checkout | 29.6 |
| torch + deps (ROCm wheel 4.8 GB + friends) | ~8 |
| F16 GGUF | 29.6 |
| Q8_0 GGUF | 15.7 |
| llama.cpp source + build tree | ~5 |
| **total** | **~88** |

That fits with ~11 GiB of slack — enough only if nothing goes wrong. `rpool` has 779 G
free, so the clean move is `pct resize 102 rootfs +200G` (additive, non-destructive)
before starting, rather than juggling intermediates mid-run.

---

## 9. Reference inference results

**None. No inference was run and no weights were downloaded.** This section is the plan
to establish the oracle, and it is a prerequisite for any implementation work.

### Setup

```bash
# in LXC 102
pip3 install --index-url https://download.pytorch.org/whl/rocm7.0 torch    # 4.8 GB
pip3 install transformers==4.57.1 safetensors huggingface_hub accelerate numpy
```

Pin `transformers==4.57.1` — the value the model was authored against, and the version
whose `modeling_deepseek_v3.py` supplies the `mscale` behaviour analysed in §3.3.

⚠️ **Inspect the remote code before running it.** `trust_remote_code=True` executes
`modeling_instella_moe.py` and `configuration_instella_moe.py` from the repo. Both were
read during this investigation and are ordinary modelling code, but they must be re-read
at the pinned revision before execution, and the revision **pinned explicitly**:

```python
AutoModelForCausalLM.from_pretrained(
    "amd/Instella-MoE-16B-A3B-Think",
    revision="e67a4a54d81b19692ec85ea1d1c777aa5c0bfd83",
    trust_remote_code=True, torch_dtype=torch.bfloat16, device_map="cuda:0")
```

### What to record

Exact package versions (`pip freeze`), ROCm version, model revision SHA, prompt string,
tokenized input IDs, generated output IDs, generated text, **first-token logits and the
top-32 token IDs + logprobs for the first 8 generated positions**, peak VRAM
(`torch.cuda.max_memory_allocated`), wall-clock tok/s, and whether the output contains any
reasoning delimiter (§3.8 flags this as unverified).

### Determinism

`do_sample=False`, `temperature=None`, `top_p=None`, `top_k=None`, fixed
`max_new_tokens`, `use_cache=True`, and a second run with `use_cache=False` to confirm the
cache path agrees with itself. Set `PYTHONHASHSEED=0`. Note that
[llama.cpp issue #14727](https://github.com/ggml-org/llama.cpp/issues/14727) reports
nondeterministic output at temperature 0 on ROCm — so the *llama.cpp side* of the
comparison may not be bit-reproducible, which is why logit comparison with tolerances
(§12) matters more than exact greedy string match.

### Prompts

1. **Reasoning/coding** (from the brief) — "Write a Go function that accepts a
   context.Context and runs four workers that process integers from a channel. Stop all
   workers immediately when the context is cancelled."
2. **Trivial completion**, to catch gross conversion errors in one token —
   "The capital of France is"
3. **Single-token logit probe** — a one-token prompt (just BOS + one token), greedy,
   `max_new_tokens=1`. This isolates the forward pass from all sampling and cache
   behaviour and is the *first* thing to compare.

### Three oracles, not one

Run the reference **three times** using the §6 staged-validation lever:

| run | config override | isolates |
|---|---|---|
| A | as-shipped (`farskip` + both sub-flags default) | the real target |
| B | `mlp_only_farskip=True` | parallel residual only, no far-skip |
| C | `attn_only_farskip=True` | far-skip only, sequential MLP |

If the llama.cpp implementation matches B and C but not A, the bug is in stream
composition. If it matches neither, the bug is upstream of FarSkip (MLA, gate, rope,
mscale). This converts a single opaque mismatch into a bisectable signal.

---

## 10. Recommended implementation approach

Ordered so that each step can fail cheaply before the next is attempted.

**Phase 0 — prepare (no model download).**
`pct resize 102 rootfs +200G`. Clone llama.cpp at a pinned upstream commit into the
container. Build unmodified for gfx1100 and smoke-test against an existing on-box GGUF
(e.g. `Qwythos-9B-…-Q8_0.gguf`, the smallest at 9.1 GiB). **Purpose: prove the build
toolchain and our own binary work before any Instella-specific variable enters.** If this
fails, everything after is unattributable.

**Phase 1 — reference oracle.** §9. Download BF16 weights once. Record all three oracles.

**Phase 2 — converter.** `conversion/instella.py`. Emit F16 GGUF. Verify: 484 tensors,
expected shapes, all metadata keys present, `gguf_dump` clean, tokenizer round-trips
against the HF tokenizer on a corpus of a few thousand strings (tokenizer mismatch is the
cheapest bug to find and the most confusing to find late).

**Phase 3 — architecture.** `src/models/instella-moe.cpp`, forked from `deepseek2.cpp`;
add the gate (copy `afmoe.cpp`'s handful of lines); thread the two residual streams.
Register the arch. Build.

**Phase 4 — correctness.** Compare F16 GGUF against the BF16 oracle: single-token logits
first, then top-k rankings, then greedy sequences, then the staged A/B/C modes. **Do not
proceed while any of these disagree beyond tolerance.**

**Phase 5 — quantize and integrate.** Q8_0. Register in llama-skein as `unlisted` with a
hand-written `cmd` pointing at a **renamed** binary (see below). Measure.

**Phase 6 — optional upstream contributions.** (a) the arch PR, following #24260's shape;
(b) the `<544, 512>` FA kernel instantiation, independently useful.

### Non-destructive integration on z4

The brief requires not disturbing the working installation. Three concrete hazards were
found in llama-skein, all avoidable without code changes:

1. **`POST /api/system/upgrade` will clobber a custom build.** It resolves its install
   destination by `pgrep -a llama-server`, **first match wins** — it does *not* parse
   `cmd` or macros (`internal/server/apiupgrade.go:110-131`). Worse than the binary swap:
   `libDir := filepath.Dir(serverPath)` then `copySharedLibs(extractDir, libDir)`
   (`internal/runtime/llamacpp_upgrade.go:165,170,281`) dumps every `.so` from the
   lemonade archive into that directory, overwriting our `libggml*.so`/`libllama.so` — so
   even restoring the `.bak` binary would leave it running against foreign libraries. And
   `restartLlamaServer()` (`:694-715`) kills **every** `pgrep -a llama-server` match.
   **→ Name the custom binary `llama-server-instella`.** Both `pgrep` and the restart
   sweep then miss it; `smokeTest` only runs `<path> --version`, so nothing on the
   lemonade side cares. The proper fix is an explicit `serverPath` config key feeding
   `opts.ServerPath`, which is a small fork change worth making later.
2. **`upgradeFromSource` clones `ggml-org/llama.cpp` only** (`:204`) — it cannot build our
   patched fork. So the source-build path is not a shipping vehicle; we build by hand.
3. **Global tuning injection has no per-model opt-out**
   (`internal/tuning/inject.go:131-165`) — it rewrites `cmd`/`env` for every model with
   `backend` `""` or `llamacpp`. Pin flash-attn explicitly in `cmd`; explicit wins
   (`apply.go:39-48`).

Additionally, the model entry **must be hand-written YAML**:
`POST /api/config/models` and `PATCH /api/config/models/{id}` cannot set `reasoning`,
`env`, `useModelName`, `filters`, `metadata`, `maxRequestTimeSecs`, `checkEndpoint`, or
`unlisted`, and `POST` **replaces the whole entry node** for an existing id
(`apiconfig.go:480`), silently dropping hand-added keys. Note also `config-schema.json`
is stale and will red-squiggle a valid `reasoning: true`.

Sketch:

```yaml
  instella-moe-16b-a3b-think-q8-0:
    name: "AMD Instella-MoE 16B-A3B Think (Q8_0) — RESEARCH ONLY"
    description: "ResearchRAIL license. Evaluation only, not for production work."
    unlisted: true              # keep it out of /v1/models for agent runners
    reasoning: true             # advertise-only; verify it actually emits a trace first
    sendLoadingState: false     # avoid interleaving synthetic reasoning_content
    env:
      - LD_LIBRARY_PATH=/opt/llamacpp-instella
    cmd: >
      /opt/llamacpp-instella/llama-server-instella --port ${PORT}
      --model /models/Instella-MoE-16B-A3B-Think-Q8_0.gguf
      --ctx-size 32768 --n-gpu-layers 99 --flash-attn off --jinja
    proxy: http://localhost:${PORT}
```

`--flash-attn off` is explicit rather than relying on `auto`, both to document the §7
finding and to stop tuning injection from setting it. `unlisted: true` is deliberate — see
§15 on the license.

---

## 11. Implementation tasks

See the OpenSpec changes authored alongside this report:

| change | repo | scope |
|---|---|---|
| `add-instella-moe-gguf-conversion` | llama.cpp fork | converter, metadata, tokenizer hash, `gguf_dump` + tokenizer-parity tests |
| `add-instella-moe-llamacpp-arch` | llama.cpp fork | arch enum, `src/models/instella-moe.cpp`, FarSkip two-stream graph, gate wiring, `test-llama-archs` entry |
| `add-instella-moe-correctness-harness` | llama-skein | reference oracle scripts, logit comparison with tolerances, staged A/B/C modes, failure-class attribution |
| `add-instella-moe-skein-deployment` | llama-skein | pinned custom-build layout, upgrade-clobber guard, hand-written model entry, benchmark runner |

Deliberately **not** combined. Conversion can be validated by tensor/metadata inspection
before any C++ exists; the harness is useful independently (it would serve any future
architecture work); and deployment is license-gated in a way the other three are not.

An optional fifth, worth doing regardless of whether Instella ships:
`add-fattn-mla-544-head-dim` (llama.cpp fork) — one kernel instantiation, restores flash
attention for any MLA model with `qk_rope_head_dim = 32`.

---

## 12. Testing strategy

### Comparison ladder — stop at the first failure

| # | Test | Tolerance | Isolates |
|---|---|---|---|
| 1 | Tokenizer parity on a few thousand strings incl. all 818 added tokens, CJK, the fullwidth-bar specials | **exact ID match** | tokenizer/pre-tokenizer, the `special: false, normalized: true` quirk |
| 2 | Prompt formatting: rendered chat template byte-compare, HF `apply_chat_template` vs `--jinja` | **exact string** | template, raw-system-prompt-after-BOS handling |
| 3 | **Single-token forward, F16 GGUF vs BF16 ref**: full logit vector | cosine ≥ 0.9999; max abs diff ≤ 0.05 | the entire forward pass, free of sampling and cache |
| 4 | Top-32 token IDs + logprobs, first 8 positions | top-1 identical; top-8 set identical; logprob diff ≤ 0.02 | graph correctness under KV growth |
| 5 | Greedy generation, 128 tokens, temp 0 | ideally exact; **treat divergence as suspect, not proof** (issue #14727) | end-to-end |
| 6 | Repeat #5 five times | self-consistent | ROCm nondeterminism, memory corruption |
| 7 | Staged A/B/C FarSkip modes (§9) | same tolerances as #3–4 | **which** of the two FarSkip changes is wrong |
| 8 | Long context: 1K/4K/8K/16K/32K prompts, compare logits at the final position | cosine ≥ 0.999 | rope/YaRN/mscale at scale, KV-cache correctness |
| 9 | Quantization delta: Q8_0 vs F16 GGUF (**not** vs BF16 ref) | perplexity delta on a fixed corpus, reported not gated | isolates quantization loss from conversion error |
| 10 | Router observability: dump top-6 expert IDs per layer for a fixed prompt, both sides | expert-ID sets identical for ≥ 95% of tokens | routing, `noaux_tc` bias, `routed_scaling_factor` |

Tests 3 and 4 are the load-bearing ones. Test 9 is deliberately against F16, not BF16 —
that is what makes quantization loss separable from conversion error, and it is only
possible because F16 fits in VRAM (§8).

### Failure-class attribution

| Symptom | Likely cause |
|---|---|
| Test 1 fails | tokenizer hash / added-token flags |
| Test 2 fails | chat template, BOS or raw-system-prompt handling |
| Test 3 fails, all positions equally wrong | tensor transposition, `kv_b` split, expert stacking order |
| Test 3 fails by a near-constant *scale* factor | **the mscale/softmax-scale trap (§3.3)** — check 0.16562688 |
| Test 3 passes, test 4 degrades with position | rope type (`rope_interleave`), YaRN, KV cache |
| Tests 3–4 pass, test 7 mode A fails but B/C pass | FarSkip stream composition |
| Test 5 diverges but 3–4 pass | sampling, or ROCm nondeterminism (#14727) — not a conversion bug |
| Test 6 inconsistent | ROCm kernel issue / memory corruption, not conversion |
| Test 8 fails only at long context | rope scaling, or the missing FA path (§7) |
| Test 9 large | genuine quantization loss — expected, report it |
| Test 10 fails | router bias, sigmoid vs softmax, `routed_scaling_factor` |

Note that "unsupported architecture" does not appear as a failure class: it cannot reach
these tests, because conversion fails cleanly at the registry (§2).

---

## 13. Risks and unknowns

| # | Risk | Severity | Mitigation / status |
|---|---|---|---|
| R1 | **FarSkip implemented subtly wrong** → fluent but incorrect output | **High** | Tests 3/4/7; the staged A/B/C oracles make it bisectable. This is the central technical risk |
| R2 | **mscale/softmax scale** silently wrong (1.874× error) | **High** | Compute 0.16562688 explicitly; test 3 catches a constant-scale error |
| R3 | **License is research-only** — blocks the stated purpose | **High, non-technical** | `unlisted: true`; do not route agent work to it; get a human decision before any production use |
| R4 | llama-skein `fitguard` silently rewrites `--ctx-size`, or refuses load with 507 | Medium | Keep all experts on GPU (which we want anyway); fit is blind to MoE offload and would over-size it (`internal/fit/fit.go:210-230`, `fitguard.go:100-113`) |
| R5 | `/api/system/upgrade` clobbers the custom build **and its shared libs** | Medium | Rename the binary `llama-server-instella` (§10) |
| R6 | No FA at head_dim 544 → long-context slowdown | Medium (perf) | Documented; optional one-line upstream fix |
| R7 | ROCm reports incorrect free VRAM ([#24906](https://github.com/ggml-org/llama.cpp/issues/24906)) → fit-engine miscalculation | Medium | Pre-existing on gfx1100, independent of Instella; worth a separate issue |
| R8 | Disk exhaustion mid-run (~88 of 99.3 GiB) | Medium | `pct resize 102 rootfs +200G` first |
| R9 | ROCm nondeterminism at temp 0 ([#14727](https://github.com/ggml-org/llama.cpp/issues/14727)) undermines greedy-match testing | Medium | Rely on logit tolerances, not exact string match |
| R10 | Whether the `-Think` model emits a reasoning delimiter is **unverified** | Low | Determine empirically in Phase 1; no `<think>` token exists in the vocab, and llama-skein has no `<think>` handling at all |
| R11 | `build_moe_ffn` may not expose the routed sum separately from the shared sum | Low–Medium | Inspect before writing the graph; assumed, not verified |
| R12 | MLA is not bug-free ([#26027](https://github.com/ggml-org/llama.cpp/issues/26027), open) | Low | Watch; unrelated arch but the same code path |
| R13 | Upstream may add Instella support first, wasting the effort | Low | Zero signals: 1 stale issue for the dense 3B, no PR, no fork, no community GGUF |
| R14 | PR #24546 (RDNA3 MoE MMQ N-tiles) may change perf characteristics for ff-1408 experts | Low | Track; benchmark after it lands |

### Explicit unknowns

- Baseline throughput on z4 has **not** been measured. `/api/fit` reports VRAM, not
  tok/s. Any Instella-vs-Qwen speed comparison requires measuring the baseline first —
  that is a task, not an assumption.
- Instella-MoE's coding/agent quality relative to Qwen3.6-35B-A3B is **unknown**. AMD
  publishes benchmarks against open-data models; I have not verified any head-to-head
  against Qwen3.6.
- Whether `is_lite`'s `n_layer()==27` coincidence causes any *unwanted* behaviour beyond
  the intended full-rank-Q path has not been checked.

---

## 14. Upstream issues and pull requests

**Existing, relevant:**

| Ref | State | Relevance |
|---|---|---|
| [llama.cpp #12270](https://github.com/ggml-org/llama.cpp/issues/12270) | closed stale 2025-04-24 | the only Instella request ever; dense 3B; never implemented |
| [llama.cpp #24906](https://github.com/ggml-org/llama.cpp/issues/24906) | open | ROCm incorrect free VRAM — affects llama-skein's fit engine now |
| [llama.cpp #14727](https://github.com/ggml-org/llama.cpp/issues/14727) | open | ROCm nondeterminism at temp 0 |
| [llama.cpp #19482](https://github.com/ggml-org/llama.cpp/issues/19482) | open | large model load hangs on ROCm |
| [llama.cpp #25620](https://github.com/ggml-org/llama.cpp/issues/25620) | open | MMVQ NaN on gfx11-generic/RDNA3.5 |
| [llama.cpp #26027](https://github.com/ggml-org/llama.cpp/issues/26027) | open | dense-MLA CUDA path subtly corrupted (GLM-5.2) |
| [llama.cpp #24546](https://github.com/ggml-org/llama.cpp/pull/24546) | open PR | RDNA3 routed-MoE MMQ N-tile sizing |
| [llama.cpp #24260](https://github.com/ggml-org/llama.cpp/pull/24260) | **merged** 2026-06-13 | **the template**: cohere2-MoE, +632/−7, 13 files |
| [llama.cpp #17945](https://github.com/ggml-org/llama.cpp/pull/17945) | merged | DeepSeek-2 YaRN log-mul fix — supplies our mscale handling |

**To file (none filed by this investigation — `gh` is unauthenticated in this session):**

1. llama.cpp feature request: *Add support for AMD Instella-MoE (FarSkip + gated MLA)* —
   should reference #12270, state that 5 of 6 features already exist, and name FarSkip as
   the single gap with `deepseek4.cpp`/`gemma3n.cpp` as precedents.
2. llama.cpp PR: *flash-attn: instantiate `<544, 512>` for MLA with `qk_rope_head_dim=32`*.
3. llama-skein issue: *`/api/system/upgrade` resolves its destination by `pgrep`, can
   clobber a custom llama-server build and its shared libs* — add an explicit
   `serverPath` config key.
4. llama-skein issue: *fit engine charges CPU-offloaded MoE experts against VRAM* —
   `internal/fit/fit.go:210-230` drops all MoE fields; MoE-aware math exists only in
   `pkg/gguf/offload.go`.
5. llama-skein issue (pre-existing, spotted incidentally): *`max_safe_ctx` can exceed
   `max_fit_ctx`* — observed live on z4 for `qwen3.6-35b-a3b-q8-0`
   (237,076 > 29,332); likely the open `bound-max-safe-ctx` change.

---

## 15. Final go/no-go recommendation

The brief asks for one of five verdicts. The honest answer depends on which goal is being
served, and these point in different directions — so both are stated rather than averaging
them into something falsely simple.

### As a fleet worker model: **do not pursue**

- **The license forbids it.** ResearchRAIL, research-only. Using it as a Skein worker
  agent on real work is production use. This alone is decisive and no engineering changes
  it.
- **The incumbent is very likely better.** `qwen3.6-35b-a3b-q8-0` is already installed and
  is the `defaultModel`: comparable active parameters (~3 B vs 2.8 B), roughly 2× total
  parameters, a much larger context window, a mature and heavily-benchmarked coding model,
  commercially usable, and requiring **zero** engineering.
- **32K context is limiting for agent work**, and long context is exactly where Instella
  is additionally penalised by the missing FA kernel.
- Instella's stated distinction is being *fully open* — data, code, and weights — not
  being better. That is a real and admirable contribution, but it is not a reason to route
  work to it.

### As an upstream capability investment: **implement now**

- Scope is bounded and unusually well-understood: ~600–800 lines against a merged template
  (#24260), **no new operators**, **no ROCm backend work**, **no new GGUF metadata keys**,
  and no `tensor_mapping.py` change.
- Every prerequisite is already on the target host — ROCm 7.2.4, `hipcc`, cmake, Python —
  which was the biggest unknown at the start and is now resolved.
- The one hard part, FarSkip, has two working precedents in master and a **staged
  validation lever** built into the model's own config that makes it bisectable rather
  than a guessing game.
- It is cleanly upstreamable with no hacks, which the brief explicitly prefers.
- FarSkip is AMD's flagship MoE-efficiency idea and will likely recur in future AMD
  models; the `<544, 512>` FA kernel benefits every MLA model with
  `qk_rope_head_dim = 32`. Both outlive this particular checkpoint.

**Complexity estimate:** Phases 0–2 (build, oracle, converter) roughly a day of focused
work each and mostly mechanical. Phase 3 (the arch) is where the difficulty sits — the
FarSkip graph is small but must be exactly right. Phase 4 (correctness) is the long pole
and could take longer than everything else combined if test 3 fails in a way the
attribution table doesn't cleanly explain. Phases 5–6 are straightforward.

**Expected performance:** I have measured nothing, so no tok/s figure is given. Directional
expectations only: decode throughput should land in the same range as the 35B-A3B
incumbent, since active parameter counts are close and decode is memory-bandwidth-bound;
prompt processing should benefit from the much smaller resident weight set; long-context
prompt processing will be *worse* than an equivalent model with FA until the 544 kernel
exists. Measuring the incumbent's tok/s is a prerequisite for any real comparison and has
not been done.

### The recommendation in one line

**Implement it as a llama.cpp contribution and a correctness-harness exercise, keep it
`unlisted` on z4, and do not put it in the agent rotation** — the engineering is a good
investment; the model is not a fleet upgrade, and its license does not permit it to be
one.

If the goal is purely "a better model on the W7800", the correct action is to stop here:
nothing in this investigation suggests Instella-MoE would beat what is already running.

---

## Appendix A — provenance

| Artifact | Identifier |
|---|---|
| Model | `amd/Instella-MoE-16B-A3B-Think` @ `e67a4a54d81b19692ec85ea1d1c777aa5c0bfd83` |
| llama.cpp upstream (survey) | `ggml-org/llama.cpp` master @ `e9fa0781f1c2`, 2026-07-28 |
| llama.cpp base of deployed binary | `956973c76466b6c791d7bdbe6eed3aa3235b2dc1`, 2026-07-15, "Fix crash with draft-simple (#25720)" |
| Deployed binary | `/opt/llamacpp-rocm-gfx110X/llama-server`, `version: 1 (956973c)`, libggml 0.16.0, built by `lemonade-sdk/llamacpp-rocm` CI |
| lemonade release cadence | `b1302` published 2026-07-20, ROCm `7.15.0a20260720`, daily |
| Reference transformers version | 4.57.1 (as declared by the model) |
| llama-skein | `investigate/instella-moe` off `main` @ `0e0301a` |
| Host ROCm | 7.2.4, HIP 7.2.53211, AMD clang 22.0.0git (roc-7.2.4) |

## Appendix B — how the "no support" claim was established

Three independent checks, so that a single stale source could not produce a false
negative:

1. **The deployed binary.** `strings libllama.so | grep -i instella` → empty;
   `grep -i -E 'farskip|far_skip'` → empty. Full arch inventory extracted from
   `src/models/*.cpp` paths embedded in the binary: **114 architectures**, no `instella*`.
2. **Current upstream master.** GitHub contents API on `src/models/` — no `instella*`,
   no `farskip*` among 200+ files. 139 arches in `llama-arch.h`. Zero `instella` hits
   across `llama-arch.{h,cpp}`, `gguf-py/gguf/constants.py`, `tensor_mapping.py`.
   `InstellaMoEForCausalLM` absent from all 196 `@ModelBase.register` names.
3. **The ecosystem.** 6 `amd/Instella-MoE-*` repos, all safetensors-only. Exactly 2
   Instella GGUFs exist, both dense 3B, 13–15 downloads each, and the author of one states
   plainly that it will not load until backend support is added. No bartowski / unsloth /
   mradermacher / lmstudio-community build. AMD's own model card lists only transformers
   and SGLang.

Conversely, the "the hard parts already exist" claim rests on positive evidence from the
deployed binary itself: MLA GGUF keys (`attention.{kv_lora_rank,q_lora_rank,
key_length_mla,value_length_mla}`) and tensors (`attn_q_a`, `attn_kv_a_mqa`,
`attn_kv_a_norm`, `attn_kv_b`, `attn_k_b`, `attn_v_b`); MoE keys (`expert_count`,
`expert_used_count`, `expert_shared_count`, `expert_gating_func`, `expert_group_count`,
`expert_group_used_count`, `expert_weights_norm`, `expert_weights_scale`) and tensors
(`ffn_gate_inp`, `ffn_{gate,up,down}_exps`, `ffn_{gate,up,down}_shexp`, `exp_probs_b`);
and `blk.%d.attn_gate` with graph node names `attn_gate_proj`, `attn_gate_sigmoid`,
`attn_gated` — i.e. the gated-attention idiom is already implemented and named.
