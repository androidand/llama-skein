# Tasks: fix-fit-resident-model-budget

## 1. Contract baseline

- [x] 1.1 Capture the current budget math: `budgetMB = VRAMFreeMB + gpuWeightMB`
      (internal/fit/fit.go:456-458) and the resident-model failure
      (qwopus-on-proxmox 80k, 96% VRAM, grow-bump still refused;
      qwen3.6-35b on z4 256k, 83% VRAM, fit suggests 26k).
- [x] 1.2 Confirm the exclusive-group escape hatch (`modelGetsWholeGPU` →
      `p.VRAMFreeMB = 0` in apifit.go) is type-specific and not generalizable
      to non-exclusive groups.

## 2. Implementation

- [x] 2.1 Detect residency in the fit path (internal/server/apifit.go): when
      `modelState(id)` reports the model loaded, pass `VRAMFreeMB = 0` so
      `fit.Analyze` budgets against `VRAMTotalMB` — the same mechanism the
      exclusive-group branch already uses.
- [x] 2.2 Leave the fit engine itself unchanged; the "never exceed the hard
      physical total" cap (fit.go:471) keeps the Qwythos regression guard intact.
- [x] 2.3 When the model is not loaded (or residency is unknown), behavior stays
      exactly as today.

## 3. Tests

- [x] 3.1 Unit tests for the fit path (internal/server/apifit_test.go):
      - modelResident: ready → true; starting/stopped/unknown → false
      - the whole-card budget fallback is already covered by TestModelGetsWholeGPU
      - stopped model: the free+weights budget path is unchanged (regression
        lock against the Qwythos over-recommend)
- [x] 3.2 Regression: full fit + server suite still green (make test-dev).

## 4. Verification

- [ ] 4.1 Live: for a resident model, PATCH ctx to a higher-but-fits value and
      verify the load succeeds and the fit report shows a sane max_fit_ctx.
- [ ] 4.2 Live: stopped-model PATCH + load path is unchanged.
