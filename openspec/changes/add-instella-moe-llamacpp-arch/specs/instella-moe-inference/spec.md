# Spec delta: instella-moe-inference

## ADDED Requirements

### Requirement: A dedicated architecture implements Instella-MoE's layer topology

llama.cpp SHALL provide an `instella-moe` architecture whose graph reproduces FarSkip: two
residual streams carried across every layer boundary, and attention and the MoE
feed-forward evaluated as a parallel block from different inputs.

#### Scenario: Two-stream residual

- **WHEN** layer `k` (for k in 2..26) is built
- **THEN** the attention branch reads the routed-free stream
- **AND** the MoE branch reads the full stream taken *before* this layer's attention output
  is added
- **AND** the layer emits a full stream equal to `input + attn + routed + shared` and a
  routed-free stream equal to `input + attn + shared`

#### Scenario: Boundary layers

- **WHEN** layer 0 is built
- **THEN** it uses a dense feed-forward network and both streams equal the token embeddings
- **AND** layer 1 receives a single stream

#### Scenario: Final normalisation

- **WHEN** the last layer completes
- **THEN** the output norm consumes only the full stream, and the routed-free stream is
  discarded

### Requirement: Attention output is gated

The architecture SHALL apply an elementwise sigmoid gate to the attention output before the
output projection, using the `attn_gate` tensor.

#### Scenario: Gate is applied

- **WHEN** attention is computed for any layer
- **THEN** its result is multiplied by `sigmoid(attn_gate · normed_attention_input)` before
  the output projection

#### Scenario: Gate tensor is mandatory

- **WHEN** a GGUF claiming this architecture omits `attn_gate`
- **THEN** loading fails with a tensor-count error rather than proceeding

### Requirement: A mis-declared checkpoint fails loudly, never silently

Loading Instella-MoE weights under a different architecture SHALL fail with a clear error
rather than producing plausible output from the wrong graph.

#### Scenario: Weights forced through the DeepSeek path

- **WHEN** a GGUF built from Instella weights declares `deepseek2` as its architecture
- **THEN** the load aborts because the emitted `attn_gate` tensor is never requested, and
  the created-versus-expected tensor counts disagree

### Requirement: All layers execute on the GPU with no operator fallback

The architecture SHALL require no ggml operators beyond those already implemented, so that
no layer falls back to CPU on the ROCm backend.

#### Scenario: Full offload on gfx1100

- **WHEN** the model is loaded with all layers requested on the GPU on a gfx1100 device
- **THEN** the backend assignment log shows every layer on the GPU
- **AND** no operator reports a CPU fallback

#### Scenario: Flash attention absence is explicit, not silent

- **WHEN** flash-attention selection is evaluated for this model's MLA cache dimension
- **THEN** the unavailability is documented and the model still produces correct output
