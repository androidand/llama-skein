# Tasks: Container-aware effective host memory limits

## Phase 1 — Resolver
- [x] 1. Add `internal/perf/cgroup_linux.go`: `EffectiveMemoryLimit()` reading
  cgroup v2 `memory.max` (walk up from `/proc/self/cgroup`) then v1
  `memory.limit_in_bytes`; returns `(limitBytes, source, ok)`; `max`/absent ⇒
  `ok=false`. Non-Linux stub returns `ok=false`.
  - Validation: `go test ./internal/perf/... -run Cgroup`
- [x] 2. Clamp `SysStat` totals/available with the limit in the unix monitor;
  keep raw fields; never count swap toward any capacity figure.
  - Validation: `go test ./internal/perf/...`

## Phase 2 — Contract + surfacing
- [x] 3. Spec-first: add `effective_total_mb`, `effective_available_mb`,
  `limit_source` to the hardware memory schema in
  `contracts/llama-skein.openapi.json`; regenerate.
  - Validation: `go generate ./pkg/apicontract && gofmt -w pkg/apicontract/llama_skein.gen.go && make check-codegen`
- [x] 4. Populate the new fields in `handleAPIHardware`
  (`internal/server/apihardware.go`).
  - Validation: `go test ./internal/server/... -run Hardware`

## Phase 3 — Consumers
- [x] 5. `hostVRAM` (`internal/server/apifit.go`) no-GPU and unified branches
  use effective figures; memory guard thresholds evaluate against the
  effective limit.
  - Validation: `go test ./internal/server/... -run 'Fit|MemGuard'`
- [x] 6. Unit tests: limited cgroup, unlimited cgroup, no cgroup, limit larger
  than physical RAM, v1 fallback.
  - Validation: `go test ./internal/perf/... ./internal/server/...`

## Phase 4 — Gate
- [x] 7. `go build ./... && go test -short ./... && make test-dev`; verify on
  z4 (LXC 102) that effective_total ≈ 48 GiB and on a bare-metal host that
  limit_source is `none`.
  - Validation: `go build ./... && go test -short ./...`
