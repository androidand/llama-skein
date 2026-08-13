# Declared placement, and cost-ordered constraint satisfaction

## ADDED Requirements

### Requirement: The fit report distinguishes a declared placement from an inherited one

The placement report SHALL carry `declared` (boolean): true when the model has a
`placement:` block, false when its placement comes only from raw flags in `cmd`.

Today a deliberate hybrid split and a stale copy-pasted `--n-gpu-layers` are
byte-identical in config, so no consumer can tell an intentional trade from a
mistake. On host A that made a 27× regression indistinguishable from a choice for
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

### Requirement: An all-layers pin is reported as an opt-out, not as full residency

Placement reporting SHALL treat an "all layers" pin — `--n-gpu-layers 99`, `999999`,
or any value at or above the model's offloadable layer count — as an undeclared
opt-out from automatic placement, exactly as it treats any other pin.

Such a pin is easy to read as harmless, because on a model that fits it produces the
same placement the planner would have chosen. It is not harmless: it disables the
whole planner for that model, so an oversized model gets no hybrid split, no
`--fit-target` reserve, and no adaptive retry ladder. Fleet audit, 2026-08-12: host A
pinned `40`, host B pinned `99`, host C pinned `99` and `999999` — every model on every
host, so `auto` was unreachable fleet-wide and `add-auto-hybrid-placement` could
never engage.

Reporting SHALL therefore key on the pin being present, not on whether its value
currently happens to be adequate.

#### Scenario: All-layers pin on a model that fits

- **WHEN** a model pins `--n-gpu-layers 999999` and fits entirely in VRAM
- **THEN** placement is reported as undeclared and pinned, even though the resulting
  placement matches what the planner would have chosen

#### Scenario: All-layers pin on an oversized model

- **WHEN** a model pins `--n-gpu-layers 999999` but is larger than the VRAM budget
- **THEN** the pin is reported as the reason no hybrid placement was planned, rather
  than the model appearing simply unfittable

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

### Requirement: A declared context is a floor the retry ladder may not cross

`RungShrinkContext` SHALL be skipped for a model whose placement is declared with
`intent: context`, and the ladder SHALL advance to the next rung rather than reduce
the model's configured context.

The floor is the model's existing `--ctx-size`. There SHALL NOT be a separate
`minContext` field: `--ctx-size` in `cmd`, `placement.minimumContext` in policy, and
`max_safe_ctx` in the fit report already express context in three places, and callers
trusting the wrong one is exactly the fleet-wide confusion `bound-max-safe-ctx`
closed. A fourth number whose only purpose is to differ from the other three adds a
way to be wrong without adding a way to be right — and `--ctx-size 200000` alongside
`minContext: 150000` is a config that contradicts itself with no way to tell which
the operator meant.

A configured context larger than the host can serve remains `bound-max-safe-ctx`'s
concern and SHALL NOT be re-handled here.

#### Scenario: Retry would shrink a declared context

- **WHEN** a memory failure escalates to `RungShrinkContext` on a model declared
  `intent: context`
- **THEN** the rung is skipped and the ladder advances, leaving the configured
  context intact

#### Scenario: Undeclared model still shrinks

- **WHEN** the same failure escalates on a model with no placement declaration
- **THEN** `RungShrinkContext` applies as it does today

### Requirement: Cheap levers are spent before expensive ones

When satisfying a declared constraint, placement SHALL prefer levers in order of
their ongoing cost, spending one-time costs before per-token ones.

Layer offload is the most expensive lever available and SHALL remain last: it is
paid on every token for the life of the process. Measured on host A, removing
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
