# Model Capability Discovery

## ADDED Requirements

### Requirement: Models report engine-sourced capabilities

Each model in `/v1/models` and `/api/models` SHALL carry a `capabilities` object
describing what the model can do. Values SHALL be sourced from the running engine
(for llama.cpp, `/props.chat_template_caps` and `/props.modalities`), never inferred
from the model id, filename, or configured flags.

The object SHALL report at least: `reasoning` (the model emits reasoning before
content), `tool_calls`, `parallel_tool_calls`, `system_role`, and `modalities`
(`vision`, `audio`, `video`).

#### Scenario: A loaded reasoning model

- **WHEN** a model whose chat template sets `supports_preserve_reasoning: true` is loaded
- **THEN** its `capabilities.reasoning` is `true`

#### Scenario: Capabilities are not guessed from the id

- **WHEN** a model id contains "think", "reasoning", or similar, but the engine
  reports `supports_preserve_reasoning: false`
- **THEN** `capabilities.reasoning` is `false` — the engine is authoritative

#### Scenario: Modalities are reported

- **WHEN** the engine reports `modalities.vision: true`
- **THEN** `capabilities.modalities.vision` is `true`

### Requirement: Capabilities survive unload and are available cold

Capabilities SHALL be cached per model file, keyed by path, size, and mtime, and
SHALL remain readable after the model is unloaded. A model that has never been
loaded SHALL report `capabilities: null`.

A caller SHALL be able to distinguish "not yet known" from "known to be false":
`null` means undetermined; an object with `reasoning: false` means determined.

#### Scenario: Model unloaded after a previous load

- **WHEN** a model that was loaded earlier is now stopped
- **THEN** its cached capabilities are still reported

#### Scenario: Never-loaded model

- **WHEN** a configured model has never been loaded
- **THEN** `capabilities` is `null`, not an object with false values

#### Scenario: Model file replaced

- **WHEN** a model's file is replaced and its size or mtime changes
- **THEN** the cached capabilities are discarded and re-probed on next load

### Requirement: A model exposes its suitability for structured output

Each model SHALL expose `prefers_structured_output`: `true` when the model can be
relied on to emit a requested structure directly, `false` when it forces reasoning
first. It SHALL be `false` whenever `capabilities.reasoning` is `true`, and `null`
whenever `capabilities` is `null`.

This exists so a caller choosing a summarizer, title generator, or any strict-format
transformation reads one field instead of re-deriving the rule.

#### Scenario: Reasoning model is not preferred for structured output

- **WHEN** `capabilities.reasoning` is `true`
- **THEN** `prefers_structured_output` is `false`

#### Scenario: Unknown capabilities yield an unknown preference

- **WHEN** `capabilities` is `null`
- **THEN** `prefers_structured_output` is `null` — callers must not read `null` as `false`

### Requirement: Backends without a capability probe report null

A backend with no implemented capability probe SHALL report `capabilities: null`.
It SHALL NOT report a partially-populated object, and SHALL NOT fail the model
listing.

#### Scenario: MLX model before MLX probing exists

- **WHEN** an MLX-backed model is listed and no MLX probe is implemented
- **THEN** `capabilities` is `null` and the listing succeeds

#### Scenario: Engine probe fails at runtime

- **WHEN** the engine is reachable but the capability probe errors or times out
- **THEN** `capabilities` is `null`, the model still lists, and the failure is logged
