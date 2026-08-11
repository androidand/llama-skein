# Crash-Loop Breaker

## ADDED Requirements

### Requirement: Persistent failures are bounded by a crash-loop breaker

When a model's backend process crashes repeatedly, the system SHALL refuse to restart after a threshold of failures within a time window, and SHALL return a descriptive error to the caller indicating the restart is refused and for how long. The error SHALL include the failure count, the window duration, and the remaining cooldown.

#### Scenario: Three crashes within the window

- **WHEN** a model's backend has crashed 3 times within the last 10 minutes and the most recent crash was less than 1 minute ago
- **THEN** the next start attempt returns an error stating the restart is refused for the remaining cooldown period

#### Scenario: Cooldown has elapsed

- **WHEN** a model's backend has crashed 3 times within the last 10 minutes but the most recent crash was more than 1 minute ago
- **THEN** the start attempt proceeds normally — the cooldown has elapsed and the system allows a retry

#### Scenario: Fewer than threshold crashes

- **WHEN** a model's backend has crashed 2 times within the last 10 minutes
- **THEN** the start attempt proceeds normally — the threshold has not been reached

### Requirement: Explicit stop resets the crash history

An explicit `Stop` (manual unload, TTL, shutdown) SHALL clear the crash history, because the operator's deliberate action resets the problem. An unexpected exit SHALL NOT clear the history.

#### Scenario: Operator unloads model

- **WHEN** a model that has crashed multiple times is explicitly stopped
- **THEN** the crash history is cleared and the next start attempt proceeds without triggering the breaker

#### Scenario: Crash after stop

- **WHEN** a model is explicitly stopped and then started, and the start fails
- **THEN** the failure is counted as the first in a new history — not additive to the pre-stop failures

### Requirement: The breaker covers all failure modes

The crash-loop breaker SHALL count not only process-level crashes (unexpected upstream exit) but also warm-up failures (process is up but not serving). Both indicate a persistent problem that won't be resolved by restarting.

#### Scenario: Warm-up failure counts toward the threshold

- **WHEN** a model's backend starts successfully but the warm-up verification fails, and this happens 3 times within the window
- **THEN** the restart is refused by the crash-loop breaker — warm-up failure is treated the same as a crash

#### Scenario: Mix of crash types

- **WHEN** a model crashes once and then fails warm-up twice within the window
- **THEN** all three failures count toward the threshold — the breaker does not distinguish between crash types

### Requirement: The breaker check happens before resource allocation

The crash-loop breaker SHALL be evaluated at the start of `doStart`, before any process launch, port binding, or GPU memory allocation. A refused restart SHALL not leave side effects (a spawned process, a held port).

#### Scenario: Refused restart leaves no side effects

- **WHEN** the crash-loop breaker refuses a restart
- **THEN** no child process is spawned, no port is bound, and no GPU memory is allocated
