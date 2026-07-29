# Spec delta: context-size-guardrails

## ADDED Requirements

### Requirement: A configured context is bounded by what the host can achieve

The model-config patch endpoint SHALL clamp a requested context size to the achievable
ceiling for that model — the smaller of the VRAM-achievable hard context and the
model's trained context — whenever that ceiling is known.

#### Scenario: A request above the trained ceiling

- **WHEN** a caller patches a context size larger than the model's achievable ceiling
- **THEN** the written value is the ceiling, not the requested value
- **AND** the response reports that the value was clamped, naming the requested value,
  the ceiling, and the model

#### Scenario: A request within the ceiling

- **WHEN** a caller patches a context size at or below the ceiling
- **THEN** the requested value is written unchanged and no clamp is reported

#### Scenario: The ceiling cannot be determined

- **WHEN** the achievable ceiling is unknown for the model
- **THEN** the requested value is written unchanged, so an unmodeled backend keeps
  working

### Requirement: The guard never raises a context size

Clamping SHALL be one-directional. It exists to prevent over-configuration and MUST
NOT be able to increase a requested value.

#### Scenario: A deliberately small context is preserved

- **WHEN** a caller patches a context far below the ceiling
- **THEN** the value is written as requested

#### Scenario: No ratchet

- **WHEN** the same context size is patched repeatedly
- **THEN** the written value is identical every time and never drifts downward

### Requirement: Both spellings of the field are honoured

The endpoint SHALL apply the same guard regardless of which field name the caller
used, since the request schema accepts the context size under more than one.

#### Scenario: Either field alone

- **WHEN** a caller sets only one of the accepted context-size fields
- **THEN** that value is subject to the guard

#### Scenario: Both fields set

- **WHEN** a caller sets both
- **THEN** the resolved value follows the endpoint's existing precedence and is
  subject to the guard
