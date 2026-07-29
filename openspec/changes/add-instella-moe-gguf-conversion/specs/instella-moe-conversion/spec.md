# Spec delta: instella-moe-conversion

## ADDED Requirements

### Requirement: Instella-MoE converts to GGUF without hand-editing the checkpoint

A `conversion/` model class SHALL convert `amd/Instella-MoE-16B-A3B-*` checkpoints to
GGUF, registered on the `architectures` entry `InstellaMoEForCausalLM`. It SHALL NOT be
registered on `model_type`, because Instella declares `model_type: "deepseek_v3"` and
registering on that value would capture genuine DeepSeek-V3 checkpoints.

#### Scenario: Converting the released checkpoint

- **WHEN** `convert_hf_to_gguf` runs against `amd/Instella-MoE-16B-A3B-Think` at revision
  `e67a4a54d81b19692ec85ea1d1c777aa5c0bfd83` with `--outtype f16`
- **THEN** conversion succeeds with no unmapped tensors and no warnings about unknown keys
- **AND** the output contains exactly 484 tensors
- **AND** `ffn_gate_inp` and `exp_probs_b` are stored as F32

#### Scenario: A real DeepSeek-V3 checkpoint is unaffected

- **WHEN** a checkpoint whose `architectures` is `DeepseekV3ForCausalLM` is converted
- **THEN** it is handled by the existing DeepSeek path, not the Instella class

### Requirement: Expert and MLA tensors are reshaped correctly

The converter SHALL collapse the 4,992 per-expert tensors into 78 three-dimensional
tensors, and SHALL derive `attn_k_b` and `attn_v_b` by splitting `kv_b_proj` on the
96-nope / 128-value boundary.

#### Scenario: Expert stacking

- **WHEN** layers 1–26 are converted
- **THEN** each emits `ffn_{gate,up,down}_exps` with a trailing expert dimension of 64
- **AND** shared experts are copied straight to `ffn_{gate,up,down}_shexp` at
  feed-forward width 2816, with no concatenation step

#### Scenario: MLA split

- **WHEN** `kv_b_proj` of shape [3584, 512] is converted
- **THEN** `attn_k_b` and `attn_v_b` are emitted with the nope rows and value rows
  separated, using the same transform as the existing DeepSeek converter

### Requirement: The YaRN attention scale is preserved exactly

The converter SHALL emit RoPE scaling metadata such that the effective attention softmax
scale reproduces the reference value derived from `mscale_all_dim`, not the naive
`1/sqrt(head_dim)`.

#### Scenario: mscale is applied

- **WHEN** the converter reads `rope_scaling.factor = 40` and
  `rope_scaling.mscale_all_dim = 1.0`
- **THEN** the emitted metadata yields an effective attention scale of 0.16562688
- **AND** a unit test asserts this value, so a regression to 0.08838835 fails the build

### Requirement: Conversion introduces no new GGUF metadata keys

The `farskip` and `gated_attention` properties SHALL be implied by the architecture
identifier rather than stored as new GGUF keys.

#### Scenario: Key audit

- **WHEN** the emitted GGUF's metadata keys are compared against the set llama.cpp already
  defines
- **THEN** every key is one that already exists upstream

### Requirement: Tokenization matches the reference exactly

The converter SHALL register the tokenizer against the existing DeepSeek-V3 pre-tokenizer
by hash, without adding new pre-tokenizer regexes.

#### Scenario: Round-trip parity

- **WHEN** a corpus of several thousand strings — including all 818 added tokens, the 800
  reserved placeholder slots, CJK text, and the fullwidth-bar special tokens — is tokenized
  by both the Hugging Face tokenizer and the GGUF vocabulary
- **THEN** the token ID sequences are identical for every string

#### Scenario: Chat template parity

- **WHEN** a conversation containing a system message and a tool call is rendered by
  Hugging Face `apply_chat_template` and by the GGUF template under `--jinja`
- **THEN** the two renderings are byte-identical
- **AND** the system message appears raw immediately after BOS with no role marker
