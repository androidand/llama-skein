# Declared placement, and cost-ordered constraint satisfaction

## ADDED Requirements

### Requirement: The fit report distinguishes a declared placement from an inherited one

The placement report SHALL carry `declared` (boolean): true when the model has a
`placement:` block, false when its placement comes only from raw flags in `cmd`.

Today a deliberate hybrid split and a stale copy-pasted `--n-gpu-layers` are
byte-identical in config, so no consumer can tell an intentional trade from a
mistake. On rocky that made a 27× regression indistinguishable from a choice for
months.

`declared` SHALL be the signal that lets `under_offloaded` (see
`flag-under-offloaded-models`) warn precisely: fire on unexplained host residency,
stay silent on a stated trade. An undeclared placement is not assumed wrong, and a
declared one is not assumed optimal — the flag reports provenance, not quality.

#### Scenario: Deliberate hybrid placement

- **WHEN** a model declares its placement and accepts host-resident weights
- **THEN** `declared` is true, the reason is reported, and no under-offloaded
  warning fires for it

#### Scenario: Inherited pin

- **WHEN** a model's placement comes from a raw `--n-gpu-layers` with no declaration
- **THEN** `declared` is false, and the model remains eligible for an
  under-offloaded warning

#### Scenario: Neither declared nor pinned

- **WHEN** a model has no placement block and no placement flags
- **THEN** `declared` is false and the planner places it, as it does today

### Requirement: latency intent refuses rather than serving slowly

A model declaring `intent: latency` SHALL NOT be planned with host-resident
weights; where no full-GPU placement exists, the plan SHALL refuse.

This SHALL respect the existing fail-open rule: only a *confident* refusal may
refuse a load. When the shape or budget is unknown, the model runs as configured
rather than being refused on a guess.

#### Scenario: Latency-critical model does not fit

- **WHEN** a model declares `intent: latency` and cannot fit fully in VRAM
- **THEN** the plan refuses, rather than silently serving it host-paced

#### Scenario: Budget unknown

- **WHEN** a model declares `intent: latency` but the VRAM budget cannot be read
- **THEN** the plan fails open and the model runs as configured

### Requirement: A declared context floor bounds the retry ladder

`RungShrinkContext` SHALL NOT reduce context below a declared `minContext`. When
the floor blocks the rung, the ladder SHALL advance to the next rung instead of
violating the declaration.

A declared floor above what the host can actually serve SHALL be refused or
reported, never advertised as achievable through `max_safe_ctx` — reopening the
fleet-wide context confusion that `bound-max-safe-ctx` closed is not acceptable.

#### Scenario: Retry would shrink below the floor

- **WHEN** a memory failure escalates to `RungShrinkContext` on a model with a
  declared floor already at that floor
- **THEN** the rung is skipped and the ladder advances, leaving the floor intact

#### Scenario: Floor exceeds host capacity

- **WHEN** a declared `minContext` is larger than the host can serve
- **THEN** it is refused or reported as unachievable, and `max_safe_ctx` does not
  advertise it

### Requirement: Cheap levers are spent before expensive ones

When satisfying a declared constraint, placement SHALL prefer levers in order of
their ongoing cost, spending one-time costs before per-token ones.

Layer offload is the most expensive lever available and SHALL remain last: it is
paid on every token for the life of the process. Measured on rocky, removing
avoidable layer offload moved three models by 7×, 8.7×, and 27×. KV quantization,
by contrast, is paid once, in quality.

A KV-quantization rung SHALL therefore sit **ahead of** `RungFullCpuMoe` in
`ladderOrder`, and SHALL be reachable only when the operator has opted in —
per-model, or through the existing `AllowKvQuantization` policy.

The existing guarantee that KV quality is never traded silently SHALL be preserved:
without an opt-in, the rung SHALL NOT fire.

#### Scenario: Operator has opted into KV quantization

- **WHEN** a declared model needs more room and permits KV quantization
- **THEN** the KV rung is tried before any layer is moved to the CPU

#### Scenario: No opt-in

- **WHEN** a model needs more room and has not permitted KV quantization
- **THEN** the KV rung is skipped entirely and KV quality is untouched

#### Scenario: Layer offload stays last

- **WHEN** the ladder escalates through its rungs
- **THEN** `RungFullCpuMoe` is attempted only after every cheaper rung is exhausted
