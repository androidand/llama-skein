# Tasks: fit-platform-correctness

- [x] 1. `perf.LatestGPUs` pure helper + unit tests (stale samples, multi-ID, sort order)
- [x] 2. `hostVRAM` pure budget function in `internal/server` + unit tests
       (multi-GPU sum, unified wired-limit cap, 70% default cap, available-memory
       free on no-GPU hosts, negative clamping)
- [x] 3. Rewire `vramMB()`, `freeVRAMBytes()`, `handleAPIContextRecommendation`,
       and the MLX fit path through the new helpers; delete the `gpuStats[0]`
       reads and `MemFreeMB` fallbacks
- [x] 4. `handleAPIHardware`: reuse `perf.LatestGPUs`; darwin chip-name fallback
       via `machdep.cpu.brand_string`
- [x] 5. Validation: `gofmt`, `go build ./internal/...`, `go test ./internal/perf
       ./internal/server ./internal/fit` — 194 tests green. Note: `go build ./...`
       and vet of the full server package are blocked by pre-existing uncommitted
       MTP WIP (`internal/server/apiconfig.go` + untracked `apiconfig_mtp_test.go`
       reference `apicontract.ConfigModelDetail_MetadataMtp`, which the committed
       generated contract does not define). Validated with that WIP stashed.
