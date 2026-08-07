# Tasks: Adaptive placement retry and learned placement profiles

Depends on: `add-auto-hybrid-placement` (planner, policy block, placement
visibility). Blocked until its Phases 1–4 land.

## Phase 1 — Failure classification
- [x] 1. `internal/process`: add `FailureClass` (gpu-oom, host-oom,
  unsupported-arch, missing-shard, invalid-flag, backend-error, crash-other)
  derived from exit code/signal + pattern table over `lastOutputLines`;
  surfaced in `LoadError` and model status.
  - Validation: `go test ./internal/process/... -run FailureClass`
- [x] 2. Fixture-driven parser tests: ROCm GPU OOM, CUDA OOM, host cgroup
  kill (137), missing GGUF shard, unknown architecture, invalid flag,
  backend unavailable — synthetic log tails, no real models.
  - Validation: `go test ./internal/process/... -run FailureClass`

## Phase 2 — Retry ladder
- [x] 3. `internal/placement`: `NextSaferPlan(prev Plan, class FailureClass)
  → (Plan, ok)` — reserve widen → batch shrink → ctx shrink (≥ minimumContext)
  → full CPU-MoE → none. Pure, unit-tested per rung.
  - Validation: `go test ./internal/placement/... -run Ladder`
- [x] 4. Supervisor wiring: on memory-class failure of an auto-placed model,
  re-plan and relaunch within an attempt cap (default 3, `placement.maxRetries`);
  ladder history recorded per model; crash-loop breaker counts attempts;
  non-memory classes never retry.
  - Validation: `go test ./internal/server/... ./internal/process/... -run Retry`
- [x] 5. Contract: ladder/attempt history + failure class in the OpenAPI
  model-status shapes; regenerate.
  - Validation: `go generate ./pkg/apicontract && make check-codegen`

## Phase 3 — Learned profiles
- [x] 6. `internal/placement/profilestore`: JSON persistence keyed by model
  identity (path+size+mtime), engine version, VRAM total, effective host
  limit, context; stores applied flags + measured peaks + load time.
  - Validation: `go test ./internal/placement/... -run Profile`
- [x] 7. Measure peaks during load/warm-up from existing perf samples; write
  profile on healthy success only (margins above reserve floor; last-rung
  barely-fits excluded).
  - Validation: `go test ./internal/server/... -run ProfileCapture`
- [x] 8. Planner consults the store first; invalidation on any key mismatch;
  fail-open to fresh planning.
  - Validation: `go test ./internal/placement/...`

## Phase 4 — Gate + acceptance
- [x] 9. `go build ./... && go test -short ./... && make test-dev`.
  - Validation: `go build ./... && go test -short ./... && make test-dev`
- [x] 10. z4 acceptance addendum: force a GPU-OOM (undersized reserve) on the
  DeepSeek-V4-Flash hybrid config, verify one ladder step recovers it, and
  verify the learned profile short-circuits the next launch. Record evidence
  in add-auto-hybrid-placement/design.md.
  - Validation: manual on z4
