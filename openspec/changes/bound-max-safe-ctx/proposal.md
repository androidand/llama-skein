# Proposal: Bound max_safe_ctx to an achievable ceiling

## Why

`/api/fit` advertises `max_safe_ctx` as "the number opencode/skein should trim
prompts to." Two cases make it advertise a budget the host cannot honor, which
is the root of the fleet context-size confusion (audit 2026-07-11):

1. **Unconfigured model, unknown VRAM → native-sized budget.** In
   `internal/fit/fit.go`, when a model has no `--ctx-size`
   (`ConfiguredCtx <= 0`, e.g. MLX models or a GGUF launched without the flag)
   `hardCtx = TrainedCtx` (the model's full native/rope-extended context), and
   the VRAM cap is only applied when `vramMaxCtx > 0` (fit.go:236). When VRAM
   can't be estimated, `hardCtx` stays at the native value and
   `max_safe_ctx ≈ 0.92 × native`. Observed live: `mlx-qwen3-coder-30b`
   reports `configured_ctx=0, max_safe_ctx=237076` on a host that cannot serve
   anywhere near that. Callers trust it and ship prompts that 413 or OOM.

2. **Under-configured model is invisible.** When an operator (or a stale
   host config) pins `--ctx-size` far below what VRAM allows — e.g. z4's
   default `qwen3.6-35b-a3b-q8-0` pinned at 3072 on a 48 GB card — the fit
   report faithfully echoes the tiny `configured_ctx` and nothing signals that
   the model is starved. The failure only surfaces when a real prompt is
   rejected, and the client UI (opencode) shows an unrelated capacity number,
   so the pin stays hidden.

The fit *math* is right; the gap is that an un-cappable estimate is emitted as
if it were achievable, and a starved config is emitted without a signal.

## What

- **Cap the unconfigured path.** When `ConfiguredCtx <= 0`:
  - If `vramMaxCtx == 0` (free-VRAM unknown), fall back to estimating
    `vramMaxCtx` from `VRAMTotalMB` rather than leaving `hardCtx` at the native
    value. If total VRAM is also unknown, set `FitLevel = "unknown"` and cap
    `max_safe_ctx` at a conservative default (`defaultUnknownCtx`) instead of
    the native context — never advertise a native/rope-extended ceiling that
    was never VRAM-checked.
- **Surface under-configuration.** When `ConfiguredCtx > 0` and a VRAM estimate
  shows the model could safely run a materially larger context
  (`vramMaxCtx ≥ ConfiguredCtx × underConfigFactor`), set a boolean
  `under_configured` on the fit result and include the achievable ceiling in
  `reason`. Emit a single `WARN` from the model-load path when a model loads
  under-configured, so operators and skein's ctx-fit sweep have a signal.
- Keep the existing "don't cap a *configured* ctx down from an estimate" rule
  (fit.go:236) intact — a running command has proven it loads; we only *warn*,
  never silently shrink a configured model.

## Non-goals

- No change to the fit math for the normal configured+known-VRAM path.
- No auto-correction of ctx-size here — growing/shrinking configs is skein's
  sweep (separate change `bidirectional-ctx-fit-sweep` in the skein repo).
- No opencode display changes (separate change in opencode-skein).

## Contract impact

- Adds an optional `under_configured` boolean and (optional) `unknown`
  `fit_level` enum value to the `ModelFit` schema in
  `contracts/llama-skein.openapi.json`. Requires `go generate ./pkg/apicontract`
  and a downstream TypeScript client regen. Additive and backward compatible.

## Validation

- Unit tests in `internal/fit`:
  - unconfigured + zero free VRAM + known total → capped by total, not native.
  - unconfigured + all VRAM unknown → `fit_level="unknown"`, `max_safe_ctx` at
    the conservative default, never native.
  - configured far below VRAM ceiling → `under_configured=true`, reason names
    the achievable ceiling.
  - configured within range → `under_configured=false`, unchanged output.
- `gofmt`, `go build ./...`, `make check-codegen`, `go test ./internal/... ./pkg/...` green.
