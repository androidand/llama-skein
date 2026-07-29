# Spec delta: model-correctness-validation

## ADDED Requirements

### Requirement: A reference oracle can be produced and replayed without a GPU

The repository SHALL provide a reference runner that captures a deterministic Transformers
oracle for a model, and SHALL persist it as artifacts that later comparisons can replay
without re-running inference.

#### Scenario: Oracle capture

- **WHEN** the reference runner executes against a pinned model revision with sampling
  disabled
- **THEN** it records the tokenized input IDs, generated output IDs, generated text, the
  full first-token logit vector, the top-32 IDs and logprobs for the first eight positions,
  peak VRAM, and tokens per second
- **AND** it records the exact package versions, ROCm version, and model revision SHA

#### Scenario: Replay

- **WHEN** a comparison runs later on a machine with no GPU
- **THEN** it loads the stored oracle artifacts and completes without invoking the reference
  implementation

#### Scenario: Remote code is reviewed before execution

- **WHEN** a model requires `trust_remote_code`
- **THEN** the runner pins an explicit revision
- **AND** the procedure records that the remote modelling code was read at that revision
  before it was executed

### Requirement: Comparison is a ladder that reports a failure class

Validation SHALL proceed through ordered tests of increasing scope, and a failure SHALL
report which layer of the stack is implicated rather than only that a mismatch occurred.

#### Scenario: Tokenizer mismatch is distinguished from graph error

- **WHEN** token ID sequences differ between the reference and the GGUF vocabulary
- **THEN** the harness reports a tokenizer failure class and does not run the logit
  comparisons

#### Scenario: A constant-scale logit error is attributed

- **WHEN** first-token logits differ from the reference by an approximately constant
  multiplicative factor
- **THEN** the harness names the attention-scale computation as the likely cause and states
  the expected effective scale

#### Scenario: Quantization loss is separable from conversion error

- **WHEN** a quantized GGUF is evaluated
- **THEN** it is compared against the full-precision GGUF, not against the reference
  implementation, so that quantization loss cannot be mistaken for a conversion defect

### Requirement: Greedy text divergence does not by itself fail a build

Exact greedy string equality SHALL be reported but SHALL NOT gate a build, because the ROCm
backend is known to be nondeterministic at temperature zero.

#### Scenario: Greedy divergence with matching logits

- **WHEN** logit and top-k comparisons pass within tolerance but greedy text differs
- **THEN** the harness reports the divergence as informational and does not fail

#### Scenario: Self-consistency check

- **WHEN** the same greedy generation is repeated five times against the same build
- **THEN** the harness reports whether the implementation agrees with itself, separately
  from whether it agrees with the reference

### Requirement: Peak memory is reported per side, never differenced

Memory figures SHALL be reported independently for each side where the reference
implementation and the inference engine use different KV-cache layouts.

#### Scenario: Divergent cache layouts

- **WHEN** the reference caches decompressed per-head keys and values while the engine
  caches a compressed latent
- **THEN** both peak-memory figures are reported with their layout named, and no difference
  between them is presented as a finding
