# Spec delta: model-loading (add-fit-load-guard)

## ADDED

### Proactive fit clamp at load

- Before the router captures per-model configuration (and before preload), the
  server MUST consult the fit engine for every configured model. When a model
  would NOT fit host memory at its configured context, the server MUST:
  - shrink the model's `--ctx-size` to the largest safe context, if one exists
    (the KV cache was the constraint); or
  - mark the model unfittable when even a minimal context won't fit (the
    weights exceed memory).
- The clamp MUST fail open: a model whose fit cannot be sized confidently
  (host VRAM not yet available, un-parseable weights, a backend the engine does
  not model) is left exactly as configured. The clamp never enlarges a context
  and never blocks a model it cannot size.

### Load refusal gate

- Before a request causes a not-yet-resident model to load, the server MUST
  refuse with `507 Insufficient Storage` (and a JSON `model_does_not_fit_error`
  body) when that model cannot fit host memory — so the backend is never
  launched. This prevents an oversized load from OOM-crashing the host
  (fatal on unified-memory hosts).
- A model already resident is not re-gated (it fit when it loaded).
- A "cannot fit" decision MUST be confident: `FitLevel` is `no`, backed by a
  known host-VRAM figure and a known model weight size. Any other state is
  treated as unknown and allowed through (fail open).

### Preload

- Startup preload MUST skip a model the load gate would refuse, since preload
  bypasses the HTTP layer and would otherwise OOM the host directly.
