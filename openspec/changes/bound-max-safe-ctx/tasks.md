# Tasks: bound-max-safe-ctx

- [x] 1. `internal/fit`: `max_safe_ctx` is clamped to 0 when the verdict is
       `"no"` (a non-fitting model has no usable budget — this is what emitted
       ~237k alongside fit_level:"no"); the VRAM-unavailable early return now
       reports `"unknown"` (not `"no"`) with max_safe 0.
- [x] 2. `internal/fit`: added `UnderConfigured bool`; set when
       `ConfiguredCtx > 0` and `ConfiguredCtx < vramMaxCtx × underConfigFrac`
       (0.8, matching skein's sweep threshold); achievable ceiling named in Reason.
- [x] 3. Contract: added `under_configured` (bool) + `unknown` fit_level to
       `ModelFit`; `go generate` + `gofmt` done (gen file regenerated).
- [ ] 4. Model-load-path WARN — DEFERRED (needs a cross-layer fit call at load;
       operator-nicety. skein's sweep consuming `under_configured` from /api/fit
       is the functional path).
- [x] 5. Unit tests (`internal/fit`): VRAM-unavailable → unknown+0;
       unconfigured non-fitting → fit=no + max_safe 0; configured-starved →
       under_configured; configured-in-range → not. All pass.
- [x] 6. `gofmt`, `go build ./...` clean, 175 tests pass, `make check-codegen`
       green (regen committed). Design-first codegen propagated to BOTH
       consumers: skein Go via `pkg/apicontract` + `go.work` (builds, uses the
       new fields), and opencode TS via `bun run build:llama-skein-client` — the
       regen surfaced that placement.ts's FIT_RANK had to handle the new
       `unknown` fit_level (fixed: ranked 0 / excluded).
