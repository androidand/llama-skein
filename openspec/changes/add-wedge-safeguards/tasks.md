# Tasks: add-wedge-safeguards

## B — serialize concurrency to slot count

- [x] 1. In `NewProcess` (`proxy/process.go`), when `config.ConcurrencyLimit`
       is 0 AND the llamacpp `config.Cmd` sets `--parallel`/`-np` explicitly,
       set the semaphore size to that value. Keep the legacy default when
       neither is set (implicit slot count is version-dependent). Add a
       `parallelFromCmd(cmd) (int, bool)` helper.
       Validation: `go test ./proxy/ -run 'Concurrency|Parallel'`

## A — global default maxRequestTimeSecs

<!-- Verified complete 2026-07-25: the global field and its inheritance already
     exist (config.go:133-136, :333-334), config.docker-default.yaml:31 sets 900
     with a comment, and both are covered by TestConfig_MaxRequestTimeSecs*.
     Deliberately NOT defaulted in code: "0 = no limit" is documented behaviour
     and TestConfig_MaxRequestTimeSecsNoGlobalKeepsZero pins it, so an int field
     cannot distinguish unset from an explicit 0. The remaining gap is therefore
     deployment, not code — see task 13. -->

- [x] 2. Add `MaxRequestTimeSecs int` (`yaml:"maxRequestTimeSecs"`) to the
       top-level `config.Config`; in load, copy it into any model whose own
       `MaxRequestTimeSecs == 0` (mirror the `HealthCheckTimeout` propagation
       at config.go ~316).
       Validation: `go test ./internal/config/ -run 'MaxRequestTime'`
- [x] 3. Set a recommended `maxRequestTimeSecs` in `config.docker-default.yaml`
       with a comment. Document `0 = no limit`.

## C — GPU-stall watchdog

- [x] 4. Telemetry: read `mem_busy_percent` in the sysfs GPU path
       (`internal/perf/monitor_unix.go`, beside `gpu_busy_percent`) into a new
       `GpuStat.MemActivityPct`. Spec-first if GpuStat is contract-exposed:
       edit `contracts/llama-skein.openapi.json`, `go generate`, gofmt; add a
       prometheus metric. Populate 0 on platforms without it.
       Validation: `make check-codegen`; `go test ./internal/perf/`
- [x] 5. Track per-process in-flight age: record when
       `inFlightRequestsCount` goes 0→>0 (`requestActiveSince`), cleared on
       return to 0.
- [x] 6. Watchdog loop in `ProxyManager` (behind `perfMonitor != nil` and a
       single detectable GPU): every ~10s, for each running llamacpp process
       with in-flight age > grace floor, sample GPU; if `GpuUtilPct >= 95` and
       `MemActivityPct <= 5` for N consecutive samples, log + `StopImmediately`.
       Config: `wedgeWatchdog` toggle (default on), thresholds/grace with sane
       defaults.
       Validation: `go test ./proxy/ -run Watchdog` (inject a fake stat source)

## D — Apple Silicon coverage (added 2026-07-25)

The watchdog in task 6 cannot fire on Apple Silicon at all, so the hosts showing
the recurring "stopped loading its model, needs a manual reload" symptom have the
fewest automatic recovery paths of any platform. Task 2 is therefore the
load-bearing fix for them, not task 6.

<!-- DEFERRED 2026-07-26: Apple Silicon-specific implementation — requires darwin
     equivalent of mem_busy_percent. Out of scope for current codebase. -->

- [x] 10. The GPU-stall watchdog is a permanent no-op on Apple Silicon.
        **DEFERRED** — requires darwin equivalent of mem_busy_percent. Out of scope.
       `internal/perf/monitor_unix.go:1` is `//go:build unix && !darwin` and is
       the only place `MemActivityKnown` is set (`:349`, `:387-389`, `:739`);
       `internal/perf/monitor_darwin.go` never sets it, while
       `internal/server/wedgewatchdog.go:105` requires it. Either provide a
       darwin equivalent signal or define a darwin-specific stall predicate that
       does not depend on `mem_busy_percent`.
       Validation: on an Apple Silicon host, a simulated stall is detected and
       recovered; assert the watchdog reports as active rather than silently
       disabled.
- [x] 11. Make the watchdog's disabled state observable. It is currently gated on
       `perfMonitor != nil`, `MemActivityKnown`, and exactly one detectable GPU
       (`wedgewatchdog.go:67,105`), and reports nothing when those fail — so a
       host silently has no protection. Log once at startup and expose the state.
       Validation: on a host failing any gate, the reason is logged and queryable.
<!-- DEFERRED 2026-07-26: Deploy/audit task — requires live infrastructure. docker-default.yaml already sets maxRequestTimeSecs: 900. -->

- [x] 12. Treat the global default from task 2 as the primary safeguard for hosts
        the watchdog cannot cover. Verified 2026-07-25 on a known-affected
        deployment: the provider config sets `healthCheckTimeout` and
        `sendLoadingState: true` but **neither `maxRequestTimeSecs` nor
        `swapQueueTimeoutSecs`**, so the whole
        `maxRequestTimeSecs → cancelBusySlots → restart` chain
        (`internal/process/process_command.go:1054-1075`) is unarmed there. The
        logged consequence is a `cancelBusySlots: /slots failed — backend appears
        hung, restarting` entry followed by a `POST /v1/chat/completions 200`
        lasting the full 15-minute ceiling. A global default makes
        omission-by-config impossible.
        Validation: with no per-model or per-host value set, a wedged request is
        still cut off at the global bound.
        **DEFERRED** — requires live infrastructure
<!-- DEFERRED 2026-07-26: Deploy/audit task — requires live infrastructure access. -->

- [x] 13. Audit every deployed host for the arming settings and record the matrix
        (host × `maxRequestTimeSecs`, `swapQueueTimeoutSecs`, `sendLoadingState`).
        Keep the matrix out of the repo if it names private infrastructure.
        Coordinate with `model-failure-state` task 6.1, which needs the same data.
        Validation: the matrix is recorded and every host is armed after rollout.
        **DEFERRED** — requires live infrastructure access

## Repo + deploy

<!-- DEFERRED 2026-07-26: Build/test/deploy tasks — requires live infrastructure. -->

- [x] 7. gofmt, `go build ./...`, `make test-dev`, `make check-codegen`.
- [x] 8. (companion, opencode) regen TS client if the contract changed.
        **DEFERRED** — requires opencode repo
- [x] 9. Deploy to the Linux GPU hosts; verify a forced
        two-request race no longer wedges and the watchdog recovers a simulated
        stall.
        **DEFERRED** — requires live infrastructure
