# Flag under-offloaded models

## ADDED Requirements

### Requirement: An under-offloaded model is reported as such

`ModelFit` SHALL carry an optional boolean `under_offloaded`, true when a model's
weights are partly host-resident (`host_resident_mb > 0`) while full GPU residency
would have fit the VRAM budget at the configured context.

The condition requires that full residency *would have fit*: a model that needs
host RAM in order to run at all SHALL NOT be flagged.

`reason` SHALL name the achievable placement, so the fix is readable off the
report without a second call. When the cause is a pinned `-ngl` below the GGUF's
offloadable layer count (`block_count + 1`), `reason` SHALL state the layer count
the host can hold.

The flag is advisory: it SHALL NOT block, delay, or alter a load, and
operator-pinned placement flags SHALL still be honoured verbatim.

#### Scenario: Pinned -ngl leaves weights in host RAM that would have fit

- **WHEN** a 65-block model with 18630 MB of weights is launched with
  `--n-gpu-layers 40` on a 24560 MB card, leaving 7165 MB host-resident
- **THEN** `under_offloaded` is true and `reason` states that the host can hold
  all 66 offloadable layers

#### Scenario: Model genuinely too large for the card

- **WHEN** a model's weights exceed the VRAM budget, so hybrid placement is the
  only way it runs
- **THEN** `under_offloaded` is false, because full GPU residency would not have fit

#### Scenario: Model fully resident on the GPU

- **WHEN** `host_resident_mb` is 0
- **THEN** `under_offloaded` is false

#### Scenario: A pin that happens to be high enough

- **WHEN** a model is pinned at `--n-gpu-layers 40` but has fewer than 40
  offloadable layers, so it is already fully resident
- **THEN** `under_offloaded` is false, because the flag decides nothing here —
  a pinned value is not by itself evidence of a problem

### Requirement: The stated remedy prefers removing the pin

`reason` SHALL state that the pin can be removed, rather than quoting a
replacement value, when `under_offloaded` is caused by a pinned placement flag and
the counterfactual plan would place the model fully on the GPU.

A raised number is another constant that goes stale against the next model, quant,
or card, and it leaves the planner disabled for that model. Where a pin carries no
deliberate intent, deleting it is the durable fix.

#### Scenario: Pin can simply be dropped

- **WHEN** a model is flagged `under_offloaded` and the counterfactual plan is full
  GPU residency
- **THEN** `reason` says the `--n-gpu-layers` pin can be removed so placement is
  computed, rather than naming a specific higher number

#### Scenario: Host RAM is genuinely needed

- **WHEN** the counterfactual plan is itself hybrid, so removing the pin would not
  yield full residency
- **THEN** `reason` describes the better hybrid split instead of advising removal

#### Scenario: A client that ignores the field

- **WHEN** a client built against the previous contract reads the fit report
- **THEN** every field it already consumed is unchanged, because
  `under_offloaded` is optional and additive

### Requirement: Loading under-offloaded warns once

The model-load path SHALL emit a single WARN when a model loads under-offloaded,
and the condition SHALL be reported through the existing config warning sources
so it appears in config warnings alongside other advisory findings.

The warning SHALL be emitted once per load, never per request.

#### Scenario: Under-offloaded model loads

- **WHEN** a model whose weights would have fit the GPU loads with host-resident weights
- **THEN** exactly one WARN naming the model and the achievable placement is emitted

#### Scenario: Model serves many requests after loading

- **WHEN** that model then serves a thousand completions
- **THEN** no further warnings are emitted for the condition

## MODIFIED Requirements

### Requirement: fit_level accounts for placement, not headroom alone

`fit_level` SHALL account for where a model's weights actually live. A model with
**avoidable** host-resident weights SHALL NOT grade above the level its
fully-resident equivalent would earn.

Previously `fit_level` graded only whether the configured placement fit within
the budget, so leaving weights in host RAM *improved* the grade by freeing VRAM.
Observed on rocky: a configuration decoding 26 of 66 layers on the CPU at
1.2 tok/s scored `"good"` with reason "fits comfortably at this context", while
the corrected full-GPU configuration — 27× faster — scored `"marginal"`. Clients
that rank providers on `fit_level` therefore preferred the slow configuration.

Grades for models that genuinely require hybrid placement SHALL be unchanged.

#### Scenario: Wasted VRAM no longer earns the better grade

- **WHEN** the same model and host are graded both under-offloaded and fully resident
- **THEN** the under-offloaded configuration does not grade above the fully
  resident one

#### Scenario: Genuine hybrid placement keeps its grade

- **WHEN** a model larger than the VRAM budget is graded
- **THEN** its `fit_level` is what it was before this change, because its
  host-resident weights are not avoidable
