# Spec delta: cap-fit-ctx-at-trained-context

## ADDED Requirements

### Requirement: The advertised grow target never exceeds the model's trained context

The fit engine's `max_fit_ctx` — the value callers (opencode-skein auto-fit, skein's
ctx sweep, the fitguard shrink target) treat as "the largest context that fits" —
SHALL be the smaller of the VRAM-achievable hard context and the model's trained
context, whenever both are known.

#### Scenario: VRAM can hold more than the model was trained for

- **WHEN** a model's `vramMaxCtx` exceeds its `TrainedCtx` (e.g. a 131k-trained model
  on a card that could VRAM-fit 576k of KV)
- **THEN** `max_fit_ctx` is `TrainedCtx`, not `vramMaxCtx`

#### Scenario: VRAM is the binding constraint

- **WHEN** `vramMaxCtx` is below `TrainedCtx`
- **THEN** `max_fit_ctx` is `vramMaxCtx` (unchanged behavior)

#### Scenario: The trained context is unknown

- **WHEN** `TrainedCtx` is unknown (0 / absent)
- **THEN** behavior is unchanged (`max_fit_ctx = vramMaxCtx`), failing open rather
  than shrinking recommendations from a lie

### Requirement: Explicit user configuration is never clamped

The fix applies to the fit engine's *recommendation/grow target* only. An operator
explicitly setting `--ctx-size` above `TrainedCtx` (e.g. 262144 where the model
supports it) SHALL keep their value untouched through the config path.

#### Scenario: User explicitly configures beyond trained context

- **WHEN** a caller writes `--ctx-size 262144` for a model whose `TrainedCtx` is 131072
- **THEN** the configured command keeps `--ctx-size 262144`
- **AND** `max_fit_ctx` in the fit report still reads the capped value (131072 or lower)

### Requirement: Under-configuration detection stays consistent

The `under_configured` flag and the fit reason string compare the configured ctx against
the capped grow target, so a model is only flagged "starved" when its context is
materially below what it could actually use — never "below 576k" for a 131k model.

#### Scenario: A model at its trained context is not "under-configured"

- **WHEN** a model runs at `--ctx-size` ≥ `TrainedCtx`
- **THEN** `under_configured` is not set, even though VRAM could hold more

## MODIFIED Requirements

(none — existing fit scenarios unchanged; the cap is a delta on `max_fit_ctx` only)