# Proposal: Fit platform correctness — perfect fit on all platforms

## Why

The fit engine (`internal/fit`) is correct, but the server layer feeds it wrong
hardware numbers on every platform class. Audit findings (2026-07-04):

1. **Stale, single-GPU VRAM sampling.** `vramMB()` (apifit.go), `freeVRAMBytes()`
   and `handleAPIContextRecommendation` (apimodels.go) all read `gpuStats[0]` —
   the *oldest buffered sample of one GPU* from the perf time-series. Multi-GPU
   hosts count one card; free-VRAM reads lag by up to the whole buffer window.
   `handleAPIHardware` already implements the correct latest-per-ID aggregation,
   duplicated inline.

2. **macOS free-memory fallback reads the wrong field.** The no-GPU fallbacks in
   apimodels.go use `sys.MemFreeMB`, which on macOS excludes inactive/purgeable
   pages and reads near zero at rest (documented on `SysStat.MemAvailableMB`).
   Context recommendation collapses to the 8192 floor on Macs without a GPU
   monitor backend.

3. **llama.cpp fit on Apple Silicon ignores the Metal wired limit.** The MLX
   path budgets against `gpuWiredLimitMB()` (else ~70% of RAM) because Metal
   will not wire the whole unified pool. The GGUF/llama.cpp path fits against
   total RAM — same Metal ceiling, no cap — so it overestimates max safe context
   and passes configs that OOM. This defeats the engine's core promise.

4. **Placeholder device name.** When no GPU monitor backend is available on
   Apple Silicon, `handleAPIHardware` reports `"Apple Silicon (unified)"`
   instead of the actual chip (`machdep.cpu.brand_string`, e.g. "Apple M3 Pro").

## What

- `perf.LatestGPUs(stats []GpuStat) []GpuStat`: pure latest-per-ID aggregation,
  sorted by ID. Single implementation used by fit, recommendations, and the
  hardware endpoint.
- `hostVRAM(sys []SysStat, gpus []GpuStat, unified bool, wiredLimitMB int)
  (total, free int)`: pure budget function. Sums latest-per-ID VRAM across GPUs.
  On unified hosts, caps total at the wired limit (else 70% of RAM) and free at
  `min(budget − gpuUsed, MemAvailableMB)`. `(*Server).vramMB()` becomes a thin
  platform wrapper.
- MLX path consumes the already-capped total (removes the double-discount risk
  of capping twice).
- `freeVRAMBytes()` and the context recommendation reuse `vramMB()` — the
  `MemFreeMB` fallbacks disappear with the duplication.
- Darwin chip-name helper for the hardware endpoint's fallback GPU entry.

## Non-goals

- No API shape changes (no OpenAPI/contract regeneration needed).
- No skein-side (fleet) changes; skein consumes the same endpoints unchanged.
- No new fit math in `internal/fit` — the engine is right; its inputs were not.

## Validation

- Unit tests for `LatestGPUs` (stale samples, multiple IDs) and `hostVRAM`
  (multi-GPU sum, unified cap, wired-limit override, no-GPU available-memory
  fallback, clamping).
- `gofmt`, `go build ./...`, `go test ./internal/...` green.
