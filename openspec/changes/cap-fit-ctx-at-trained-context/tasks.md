## 1. Contract baseline and design

- [x] 1.1 Record the current behavior: `fit.MaxFitCtx = vramMaxCtx` (internal/fit/fit.go)
      with no TrainedCtx cap; document the Muse Glimmer reproduction (z4, 131k trained,
      393216 written).
- [x] 1.2 Confirm all consumers of `MaxFitCtx` are read-only (fit report, hypothetical
      fit, fitguard shrink target) so changing its value changes no write semantics.

## 2. Implementation

- [x] 2.1 Cap `res.MaxFitCtx` at `TrainedCtx` when both are known, in
      internal/fit/fit.go (the `if vramMaxCtx > 0` block). Fail open when
      TrainedCtx <= 0.
- [x] 2.2 Keep the under-configured comparison and reason string working against
      the capped value; adjust the comment describing `MaxFitCtx`.

## 3. Tests

- [x] 3.1 Unit tests in internal/fit/fit_test.go:
      - VRAM > TrainedCtx → MaxFitCtx == TrainedCtx
      - VRAM < TrainedCtx → MaxFitCtx == vramMaxCtx (unchanged)
      - TrainedCtx unknown → MaxFitCtx == vramMaxCtx (fail-open)
      - under_configured not set when ConfiguredCtx >= TrainedCtx
- [x] 3.2 Regression: existing fit tests still green (make test-dev).

## 4. Verification

- [ ] 4.1 Re-run `/api/fit` for a Muse Glimmer model on a large-VRAM host; confirm
      max_fit_ctx ≈ trained_ctx (not VRAM-inflated).
- [ ] 4.2 Confirm an explicitly configured ctx above trained is untouched in config.
