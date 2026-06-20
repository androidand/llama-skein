# Tasks: Verified readiness for MLX/vLLM

Beads: skein-69qu (bug). Related: skein-uyyu (fit engine — limits truth).

## Phase 1 — Gate readiness on warm-up
- [ ] 1. In `doStart` (internal/process/process_command.go), for non-llamacpp
  backends: on warm-up failure, `abort()` (kill process + return error) instead
  of logging a warning and returning success. Readiness now requires a verified
  inference.
  - Validation: `go test ./internal/process -run Readiness`
- [ ] 2. Test: mock upstream whose `/health` is 200 but whose
  `/v1/chat/completions` (warm-up) fails → process must NOT reach `StateReady`;
  `Run` returns an error.
  - Validation: `go test ./internal/process -run Readiness`

## Phase 2 — Post-ready wedge + slot interaction
- [ ] 3. Verify `inferenceProbeLoop` drives a wedged-but-alive process to
  `stopped` (state stops reporting ready). Add/confirm a test.
  - Validation: `go test ./internal/process -run InferenceProbe`
- [ ] 4. Review `mlxSlot` vs probe/stuck-generation: a held slot must not block
  real requests indefinitely (the 30s hang). Ensure ServeHTTP slot acquire honors
  request context (already does) and the probe never deadlocks the slot.
  - Validation: `go test ./internal/process -run MLXSerial`

## Phase 3 — Crash-loop accounting
- [ ] 5. Count repeated verified-readiness (warm-up) failures toward the
  crash-loop breaker so clients get the explicit "refusing restart" error rather
  than a per-request start+fail cycle.
  - Validation: `go test ./internal/process -run CrashLoop`

## Phase 4 — Ship + live-test
- [ ] 6. `go build ./... && go test -short ./...`; deploy M3 + M5; live-test:
  load MLX, idle past any wedge, confirm `/running` never reports `ready` for a
  non-serving model and a real request either serves or returns a clear error
  (never a silent 14.8s cold-load or 30s hang behind a `ready` lie).
  - Validation: live curl on M3/M5
