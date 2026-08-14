# Spec delta: fix-fit-resident-model-budget

## ADDED Requirements

### Requirement: A resident model is budgeted against its own VRAM ceiling, not "free + own weights"

When the fit engine scores a model that is currently loaded on the host, the VRAM
budget SHALL be the model's total-card ceiling (falling back to `VRAMTotalMB`),
not `VRAMFreeMB + modelWeights`. This prevents the resident model's own KV cache
and overhead from being silently dropped from the budget, which currently collapses
`max_fit_ctx` to 0 or a tiny fraction of what demonstrably runs.

#### Scenario: A resident model asks to grow its context

- **WHEN** a loaded model (e.g. qwopus at 80k, 96% VRAM) is fit-scored for a
  larger context
- **THEN** `max_fit_ctx` is computed against the card's total VRAM
- **AND** the value reflects what the model could actually hold, not ~0

#### Scenario: A resident model is already using most of the card

- **WHEN** a loaded model (e.g. qwen3.6-35b at 256k, 83% VRAM) is fit-scored
- **THEN** the reported `max_fit_ctx` is not collapsed to a tiny fraction of the
  configured context

#### Scenario: A stopped model is scored

- **WHEN** the scored model is not currently loaded
- **THEN** behavior is unchanged (`VRAMFreeMB` is the correct near-total figure)

### Requirement: The load guard no longer refuses a resident model's context bump on a phantom budget

The load guard's refusal path consumes the same fit figures. With the budget fix,
a model already resident and running at its configured context MUST NOT be refused
for a context that fits the card's total VRAM, purely because the free figure
already excluded the model's own residency.

#### Scenario: Bumping a resident model's context

- **WHEN** a caller writes a larger `--ctx-size` for a loaded model whose total
  VRAM requirement (weights + new KV) fits the card
- **THEN** the load is not refused as "will not fit"

## MODIFIED Requirements

(none — `fit-platform-correctness` hardware-feeding behavior is unchanged)