# Tasks: Crash-Loop Breaker

## 1. Sliding window and threshold

- [x] 1.1 `internal/process/process_command.go:60-69`: define `crashLoopWindow` (10 min), `crashLoopThreshold` (3), `crashLoopCooldown` (1 min) as package constants. Verify: constants are exported as comments, not as config knobs (non-goal: not per-model configurable).
- [x] 1.2 `internal/process/process_command.go:202-206`: add `crashMu sync.Mutex` and `crashTimes []time.Time` to `ProcessCommand`. Verify: `go build ./internal/process/`.
- [x] 1.3 Implement `recordUnexpectedExit()`: append current time, prune entries older than `crashLoopWindow`, return count. Verify: `go test ./internal/process/ -run RecordUnexpectedExit -count=1` asserts count increments and old entries are pruned.
- [x] 1.4 Implement `clearCrashHistory()`: clear `crashTimes` under `crashMu`. Verify: `go test ./internal/process/ -run ClearCrashHistory -count=1` asserts history is nil after clear.

## 2. Breaker check in doStart

- [x] 2.1 `internal/process/process_command.go:571-582`: implement `crashLoopError()` — returns non-nil when threshold reached and cooldown not elapsed. Verify: `go test ./internal/process/ -run CrashLoopError -count=1` asserts error returned at threshold, nil below threshold, nil after cooldown.
- [x] 2.2 Call `crashLoopError()` at the top of `doStart()` before any resource allocation. On error, return after a brief delay (250ms) to allow `WaitReady` callers to register. Verify: `go test ./internal/process/ -run DoStartCrashLoop -count=1` asserts no process spawned when breaker fires.
- [x] 2.3 The 250ms delay in 2.2 prevents a race: `baseRouter.doSwap` issues `WaitReady` right after `Run`, and an instant failure could resolve the whole `Run` before that waiter lands, stranding it. Verify: the existing `doSwap` tests still pass.

## 3. Wire up failure recording

- [x] 3.1 `internal/process/process_command.go:371-375`: call `recordUnexpectedExit()` in the `cmdDone` case (unexpected upstream exit). Verify: existing test coverage in `TestProcessCommand_*` asserts crash recording on unexpected exit.
- [x] 3.2 `internal/process/process_command.go:845-846`: call `recordUnexpectedExit()` when warm-up fails. Verify: warm-up failure counts toward the breaker (test from 1.3 covers the counting path; integration test verifies the warm-up path calls it).
- [x] 3.3 `internal/process/process_command.go:564`: call `clearCrashHistory()` on explicit `Stop`. Verify: `go test ./internal/process/ -run StopClearsCrashHistory -count=1` asserts history cleared after explicit stop.

## 4. Clear streak on success

- [x] 4.1 `internal/process/process_command.go:440`: `clearFailureStreak()` is already called on successful start (from `model-failure-state`). The crash history is intentionally NOT cleared on success — a model that crashes, recovers, then crashes again should have the history preserved so the breaker can fire on the next threshold. Verify: existing tests confirm this behavior.

## 5. Error reaches the caller

- [x] 5.1 The `crashLoopError` string includes: model ID, failure count, window duration, remaining cooldown, and action items (check logs, system memory, or unload to reset). Verify: `go test ./internal/process/ -run CrashLoopError -count=1` asserts error message contains all elements.

## 6. Verification

- [x] 6.1 `go build ./... && go vet ./...` clean. Verify: no new lint or vet issues.
- [x] 6.2 End-to-end against a real host: configure a model to fail to start, trigger the breaker, confirm the descriptive error appears in the swap response and logs. Record commands and observed output. **DEFERRED** — requires live host.
