# Muse Glimmer 30B as a multi-artifact package

The original free-form spec is kept verbatim as `research-spec-original.md`; this is
its delta-format successor, with the four delivered requirements recorded as such.

## ADDED Requirements

### Requirement: A declared context length may be overridden

A model whose GGUF understates its supported context SHALL be servable at the larger
context without editing the weights.

Muse Glimmer declares `context_length 131072` and supports 262144. No `--override-kv`
support exists anywhere in the tree today, so the larger window is unreachable.

The override SHALL cover the drafter's own declared length as well
(`dflash.context_length`), because a drafter capped at the smaller window caps
speculative decoding regardless of what the main model allows.

Fit SHALL be computed against the **overridden** context, not the declared one — a
model reporting a context it cannot serve is the failure `bound-max-safe-ctx` closed,
and an override must not reopen it.

#### Scenario: Model served beyond its declared context

- **WHEN** a model declaring 131072 is configured with an override to 262144
- **THEN** `/api/fit` reports the overridden value as `configured_ctx`, and a prompt
  beyond 131072 is served rather than rejected

#### Scenario: Drafter is overridden alongside the model

- **WHEN** a model with a DFlash drafter is overridden to a larger context
- **THEN** the drafter's declared context is overridden to match

#### Scenario: Override exceeds what the host can hold

- **WHEN** the overridden context does not fit the host's VRAM budget
- **THEN** fit reports it as unachievable rather than advertising it through
  `max_safe_ctx`

### Requirement: A companion artifact must earn its memory

A companion artifact attached by default SHALL have a measured benefit on the
hardware it is attached for, or SHALL NOT be attached by default.

Measured on host A: the DFlash drafter yields **34.5 tok/s against 34.6 without it**,
for 1.63 GB of VRAM — no gain, and a `[spec] failed to measure draft model memory`
warning at every load. It was attached on the research's recommendation, which
predated any measurement on this hardware.

Where a companion's memory cannot be measured, the system SHALL say why once rather
than emitting the same warning on every load.

#### Scenario: Drafter shows no measured gain

- **WHEN** a drafter is measured to give no throughput benefit on a host
- **THEN** it is not attached by default there, and its VRAM is reclaimed

#### Scenario: Drafter memory cannot be measured

- **WHEN** the drafter's memory footprint cannot be determined
- **THEN** the reason is reported once, not repeated at every load

## Delivered elsewhere

These were requirements of this change and have since shipped. Recorded so the change
does not imply unbuilt work; no delta is claimed for them.

- **Package discovery and download** (main + drafter + projector) —
  `host-model-management-api` §3–4.
- **Fit accounts for companion weights** — `internal/fit/fit.go:162-163`
  (`DraftMB`, `ProjectorMB`).
- **Companion flags and spec type** — `injectCompanionFlags`, and `draft-mtp` vs
  `draft-dflash` at `internal/server/apiconfig.go:240-251`.
- **Config records companion paths** — `ModelConfig.DraftModelPath` /
  `ProjectorPath`.
