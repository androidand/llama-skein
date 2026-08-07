# Tasks: Automatic hybrid GPU + system-RAM model placement

Depends on: `add-container-memory-limits` (effective host RAM budget).

## Phase 1 — Hybrid-aware fit engine
- [x] 1. `internal/fit`: extend `ModelShape` (expert bytes total/per-layer,
  MoE dims), `Params` (`Offload offload.Spec`, `NGpuLayers`), `Result`
  (`GPUResidentMB`, `HostResidentMB`, placement class). `AnalyzeShape`
  subtracts CPU-resident expert bytes from the VRAM-side weight term and
  budgets them against host RAM instead.
  - Validation: `go test ./internal/fit/...`
- [x] 2. `ShapeFromGGUF` populates expert bytes via
  `pkg/gguf.ExpertWeightBytes` (+ dimensional fallback); `fitForModel`
  (`internal/server/apifit.go`) parses `-ncmoe/-cmoe/-ot/-ngl` via
  `offload.For(backend).Parse` and feeds `Params`.
  - Validation: `go test ./internal/server/... -run Fit`
- [x] 3. Unify KV math: `RecommendCpuMoe` takes a KV-bytes-per-token input
  from the fit engine (cache-type aware) instead of FP16-only
  `gguf.KVCacheBytes`; the HTTP recommendation handler passes it.
  - Validation: `go test ./pkg/gguf/... ./internal/server/... -run Offload`
- [x] 4. Descriptor/hypothetical path: per-variant verdicts gain
  `placement` (`gpu|hybrid|cpu|no`) with estimated GPU/host split so
  pull/gallery ranking can distinguish "hybrid" from "won't fit".
  - Validation: `go test ./internal/fit/... ./internal/server/... -run Hypothetical`

## Phase 2 — Placement planner
- [x] 5. New `internal/placement`: `Plan(shape, gpuBudget, hostBudget,
  policy) → Plan{Mode, FlagOps, Estimate{gpu,host,kv}, PerfClass, Reason}`.
  Modes `gpu|hybrid|cpu|refuse`; MoE-hybrid pins `--n-cpu-moe` +
  `--fit-target`; dense-hybrid delegates to upstream `--fit` (unset `-ngl`,
  set `--fit-target`/`--fit-ctx`); any user-pinned placement flag ⇒ `custom`
  (no-op). Draft-model flags are cost-counted, never auto-toggled.
  - Validation: `go test ./internal/placement/...`
- [x] 6. Planner unit tests with synthetic shapes: full-GPU fit, MoE hybrid,
  dense hybrid, CPU-only, refuse (host reserve breach), custom passthrough,
  exclusive vs shared GPU budget, KV-quantization policy respected, draft
  cost counted, minimum-context floor.
  - Validation: `go test ./internal/placement/...`

## Phase 3 — Policy config
- [x] 7. `placement:` block in `internal/config` (+ `config-schema.json` +
  `config.example.yaml`): `mode` (default `auto`), `hostReserveGiB`
  (default max(12, 10%)), `gpuReserveGiB` (default max(2, 5%)),
  `minimumContext`, `allowKvQuantization` (default false). Per-model
  override via existing model `metadata`/fields precedent.
  - Validation: `go test ./internal/config/...`

## Phase 4 — Application + guards
- [x] 8. `applyAutoPlacement()` in `internal/server`, called with
  `clampModelsToFit()` at boot/reload after tuning injection: in-memory
  `applyFlagOps` per model, original cmd preserved, fail-open, logged plan.
  - Validation: `go test ./internal/server/... -run Placement`
- [x] 9. Fit guard third remedy: before refusing/marking unfittable, consult
  the planner; only confident `refuse` plans produce 507/unfittable. Preload
  and prompt-guard `maxSafeCtx` consume hybrid-aware results.
  - Validation: `go test ./internal/server/... -run 'FitGuard|Preload|PromptGuard'`
- [x] 10. Preflight: run `llama-fit-params` (when present next to the engine
  binary; ensure upgrade path marks tool binaries executable) with the
  planned args; parse output as the effective-args report; downgrade to
  plan-only (no hard fail) when the tool is missing.
  - Validation: `go test ./internal/server/... -run Preflight`
- [x] 11. Guard compatibility: wedge-watchdog + `maxRequestTimeSecs` docs and
  defaults reviewed for `cpu-bound-hybrid` (slow ≠ wedged); memory-guard
  admission uses the plan's host-bytes estimate.
  - Validation: `go test ./internal/server/... -run 'Wedge|MemGuard'`

## Phase 5 — Contract + visibility (spec-first)
- [x] 12. `contracts/llama-skein.openapi.json`: placement fields on
  `ModelFit`/fit responses (mode, gpu/host estimate, perf class, applied
  flags, reason), placement block on config add/patch responses; regenerate
  Go.
  - Validation: `go generate ./pkg/apicontract && make check-codegen`
- [x] 13. Model listings expose effective placement: `/api/models` records
  gain a `placement` hint (mode, perf class, applied, reason) and the offload
  flag read-back reflects the rewritten in-memory command. (Add/patch
  responses do NOT report a plan: placement is computed at reload, after the
  write response — callers read `/api/fit/{model}` post-reload instead.)
  - Validation: `go test ./internal/server/... -run 'Models|Placement'`
- [x] 14. UI (ui-svelte): model rows show a placement badge ("Hybrid GPU +
  RAM" / "System RAM only" / "Insufficient safe memory") with reason +
  perf-class tooltip. Estimated GPU/host split and effective llama.cpp args
  stay API-side (`/api/fit/{model}`) for now.
  - Validation: `make test-ui`

## Phase 6 — Gate + docs
- [x] 15. `go build ./... && go test -short ./... && make test-dev && make
  test-all`; push origin before any skein/opencode consumption.
  - Validation: `go build ./... && go test -short ./... && make test-dev`
- [x] 16. Docs: `docs/placement.md` — how auto placement works, why file size
  ≠ RAM, why VRAM+RAM can't be summed without reserves, dense vs MoE, modes,
  policy config, container limits, inspecting/overriding the generated args.
  - Validation: manual read-through
- [x] 17. Acceptance (manual, z4): raise LXC 102 memory, pull
  `DeepSeek-V4-Flash-0731-GGUF` UD-IQ2_M, verify auto `hybrid` plan +
  successful chat completion + recorded VRAM/RAM peaks; then load a small
  model and verify untouched full-GPU settings. Compare `--cpu-moe` vs pinned
  `--n-cpu-moe` vs pure `--fit` delegation; record measured numbers in
  design.md.
  - Validation: manual on z4, evidence in design.md

## Phase 7 — Clients (deferred, mirrors add-model-offload-tuning 9–10)
- [D] 18. opencode: regen TS client; placement surfaced in status/routing.
- [D] 19. skein: re-pin; supervisor consumes placement in provider scoring.
