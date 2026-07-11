# Spec delta: Bound max_safe_ctx to an achievable ceiling

## Changes

- MODIFIED: for a model with no configured `--ctx-size` (`configured_ctx <= 0`),
  `max_safe_ctx` is bounded by a VRAM estimate. When free VRAM is unknown the
  estimate falls back to **total** VRAM; when total VRAM is also unknown the fit
  result reports `fit_level = "unknown"` and `max_safe_ctx` is capped at a
  conservative default. Previously the hard ctx fell back to the model's full
  trained/native context and, if VRAM could not be estimated, `max_safe_ctx`
  was advertised at ~0.92× that native context — a ceiling the host had never
  been shown to honor.

- ADDED: `ModelFit.under_configured` (boolean). True when a model has a
  positive `configured_ctx` but the VRAM estimate shows it could safely run a
  materially larger context (`vram_max_ctx >= configured_ctx ×
  underConfigFactor`). `reason` names the achievable ceiling. The model-load
  path emits a single WARN when a model loads under-configured.

- ADDED: `"unknown"` as a `fit_level` value, used only when neither free nor
  total VRAM can be estimated for an unconfigured model.

## Invariants preserved

- A **configured** (deployed) model is never silently shrunk: the existing rule
  that a configured ctx is not capped down from an estimate stays; the change
  only *warns* and *reports* `under_configured`.
- `max_safe_ctx` never exceeds the per-request hard ctx (`configured_ctx /
  parallel_slots`) for configured models.
- API remains backward compatible: `under_configured` is optional and
  `fit_level="unknown"` is additive.
