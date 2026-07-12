# Tasks: add-wedge-safeguards

## B — serialize concurrency to slot count

- [ ] 1. In `NewProcess` (`proxy/process.go`), when `config.ConcurrencyLimit`
       is 0 AND the llamacpp `config.Cmd` sets `--parallel`/`-np` explicitly,
       set the semaphore size to that value. Keep the legacy default when
       neither is set (implicit slot count is version-dependent). Add a
       `parallelFromCmd(cmd) (int, bool)` helper.
       Validation: `go test ./proxy/ -run 'Concurrency|Parallel'`

## A — global default maxRequestTimeSecs

- [ ] 2. Add `MaxRequestTimeSecs int` (`yaml:"maxRequestTimeSecs"`) to the
       top-level `config.Config`; in load, copy it into any model whose own
       `MaxRequestTimeSecs == 0` (mirror the `HealthCheckTimeout` propagation
       at config.go ~316).
       Validation: `go test ./internal/config/ -run 'MaxRequestTime'`
- [ ] 3. Set a recommended `maxRequestTimeSecs` in `config.docker-default.yaml`
       with a comment. Document `0 = no limit`.

## C — GPU-stall watchdog

- [ ] 4. Telemetry: read `mem_busy_percent` in the sysfs GPU path
       (`internal/perf/monitor_unix.go`, beside `gpu_busy_percent`) into a new
       `GpuStat.MemActivityPct`. Spec-first if GpuStat is contract-exposed:
       edit `contracts/llama-skein.openapi.json`, `go generate`, gofmt; add a
       prometheus metric. Populate 0 on platforms without it.
       Validation: `make check-codegen`; `go test ./internal/perf/`
- [ ] 5. Track per-process in-flight age: record when
       `inFlightRequestsCount` goes 0→>0 (`requestActiveSince`), cleared on
       return to 0.
- [ ] 6. Watchdog loop in `ProxyManager` (behind `perfMonitor != nil` and a
       single detectable GPU): every ~10s, for each running llamacpp process
       with in-flight age > grace floor, sample GPU; if `GpuUtilPct >= 95` and
       `MemActivityPct <= 5` for N consecutive samples, log + `StopImmediately`.
       Config: `wedgeWatchdog` toggle (default on), thresholds/grace with sane
       defaults.
       Validation: `go test ./proxy/ -run Watchdog` (inject a fake stat source)

## Repo + deploy

- [ ] 7. gofmt, `go build ./...`, `make test-dev`, `make check-codegen`.
- [ ] 8. (companion, opencode) regen TS client if the contract changed.
- [ ] 9. Deploy to proxmox + z4 (+ rocky when online); verify a forced
       two-request race no longer wedges and the watchdog recovers a simulated
       stall.
