# Crash-Loop Breaker

## Why

When a model's backend process fails to start or crashes immediately after starting, the system retries on the next request. This is the correct behaviour when the failure is transient — a port clash, a momentary resource pressure, a one-off OOM. But when the root cause is persistent — a misconfigured model path, an OOM that won't resolve until the model is unloaded — every request triggers another attempt, each one crashing, each one cold-starting, each one consuming GPU memory and time with no chance of success.

The observable symptom is a request that hangs for the duration of the health-check timeout, fails, and returns an opaque error. If the caller retries, the cycle repeats. The only visible signal is repeated error log lines; the caller receives no indication that the situation is unrecoverable without intervention.

The `model-failure-state` change made failure a distinct, restartable state (`StateFailed` + `StartableFrom`). That fix is necessary — a failed model should be restartable. But it's insufficient without a circuit breaker: the restart path must be bounded so that persistent failures surface a clear, actionable error instead of looping.

This change adds that bound.

## What Changes

- **A sliding window of unexpected exits.** The process tracks timestamps of each unexpected exit (crash, health-check timeout, warm-up failure) within a configurable window. An explicit `Stop` (manual unload, TTL, shutdown) clears the history — the operator's deliberate reset.
- **A threshold + cooldown.** When the window contains at least `crashLoopThreshold` exits (default 3 in 10 minutes), and the most recent exit was less than `crashLoopCooldown` ago (default 1 minute), the system refuses to restart and returns a descriptive error. The error reaches the caller through the normal swap-failure path.
- **Coverage of all failure modes.** The breaker counts not only process-level crashes (upstream exit) but also warm-up failures (process is up but not serving) — both indicate a persistent problem that won't be resolved by restarting.

## Capabilities

### New Capabilities

- `crash-loop-breaker`: the sliding-window tracker, threshold/cooldown logic, and the descriptive "refusing restart" error returned to callers.

### Modified Capabilities

- `model-failure-state`: a failed model that is restartable now has a bounded restart path.

## Non-Goals

- **Not** an automatic recovery mechanism. The breaker surfaces the problem; it does not attempt to fix it (e.g., by trying a different GPU or downgrading the offload). Recovery requires operator action (unload, fix config, restart service).
- **Not** configurable per-model. The threshold, window, and cooldown are process-package-level constants. Per-model tuning would add configuration surface for a condition that should be rare and always indicates a problem.
- **Not** a replacement for the `healthCheckTimeout` or `maxRequestTimeSecs` settings. Those prevent individual hangs; the breaker prevents the retry loop that follows.

## Impact

- `internal/process/process_command.go` — `crashLoopWindow`, `crashLoopThreshold`, `crashLoopCooldown` constants; `crashTimes`/`crashMu` fields on `ProcessCommand`; `recordUnexpectedExit()`, `clearCrashHistory()`, `crashLoopError()` methods; breaker check in `doStart()`; `clearCrashHistory()` on explicit `Stop`.
- `internal/router/base.go` — the `doSwap` restart path now receives a descriptive "refusing restart" error instead of silently cold-starting.
