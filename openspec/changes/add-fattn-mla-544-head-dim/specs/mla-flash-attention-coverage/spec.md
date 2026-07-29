# Spec delta: mla-flash-attention-coverage

## ADDED Requirements

### Requirement: Flash attention is available for MLA caches of width 544

The flash-attention kernel selection SHALL include the key/value dimension pair produced by
a multi-head latent attention model with a 512-wide latent and a 32-wide rotary key, so that
such models are not silently excluded.

#### Scenario: Kernel is selected

- **WHEN** flash-attention selection runs for a cached key width of 544 and value width of
  512
- **THEN** a kernel is selected rather than reporting no available kernel

#### Scenario: Neighbouring dimensions are unaffected

- **WHEN** models with already-supported cache widths are evaluated
- **THEN** their kernel selection is unchanged

### Requirement: Correctness is established before any performance claim

Enabling flash attention for a new dimension SHALL be validated numerically before its
performance is reported.

#### Scenario: Logit agreement with flash attention disabled

- **WHEN** the same model and prompt are evaluated with flash attention enabled and disabled
- **THEN** the resulting logits agree within the tolerance used for existing flash-attention
  paths

#### Scenario: Measured speedup only

- **WHEN** a performance change is reported for this dimension
- **THEN** it is a measured figure from the target device
- **AND** where the selected kernel on that device is not the matrix-accelerated path, this
  is stated so no larger gain is implied

### Requirement: The RDNA3 large-head-dimension guard is left intact

This change SHALL NOT alter the existing head-dimension cap that steers RDNA3 devices away
from the matrix-accelerated attention kernel.

#### Scenario: Guard unchanged

- **WHEN** an RDNA3 device evaluates a query head dimension above the existing cap
- **THEN** it still falls back to the tile kernel exactly as before
