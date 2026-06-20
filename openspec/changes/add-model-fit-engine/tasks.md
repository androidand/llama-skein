# Tasks: Model-fit engine and /api/fit

Beads epic: see `skein` bd epic (linked on creation) — also the cross-repo spine for
the downstream `skein` and `opencode-skein` changes.

Reference implementation: `~/dev/llmfit/llmfit-core/src/{fit,hardware,models}.rs` and
`~/dev/llmfit/llmfit-core/data/*.json` (MIT — attribute in vendored files).

## Phase 1 — Catalog + types (data foundation)
- [ ] 1. Vendor the full catalog under `internal/fit/data/` (`hf_models.json`,
  `benchmark_cache.json`, `baselines.json`, `docker_models.json`) with a LICENSE/ATTRIBUTION
  note; embed via `go:embed`.
  - Validation: `go test ./internal/fit -run Catalog` (loads + counts records)
- [ ] 2. Port the catalog types (`LlmModel`, `Capability`, `ModelFormat`, `UseCase`,
  `ModelDatabase`) and a loader/index over the embedded data.
  - Validation: `go test ./internal/fit -run Catalog`

## Phase 2 — Hardware spec
- [ ] 3. Define a fit-facing `SystemSpecs` (backend, unified-memory, total/usable VRAM,
  RAM, cores) built from the existing `internal/perf` snapshot — reuse the vm_stat /
  kernel-pressure work, don't re-detect.
  - Validation: `go test ./internal/fit -run SystemSpecs`

## Phase 3 — Fit scorer (the core port)
- [ ] 4. Port `fit.rs`: `Analyze(model, system, cfg) → ModelFit` — memory-headroom fit,
  run mode (gpu/tensor-parallel/moe-offload/cpu-offload/cpu-only), four-dimension weighted
  score, fit level, and max-safe-context. Mirror llmfit's margins (e.g. 1.2× headroom).
  - Validation: `go test ./internal/fit -run Analyze` (golden cases below)
- [ ] 5. Port the tok/s estimate from benchmark baselines (`bench`/`benchmarks` lookup).
  - Validation: `go test ./internal/fit -run Throughput`
- [ ] 6. Calibration golden tests against known real outcomes: 18B-Q8 on 24 GB → ~49k ctx
  safe / 98k fatal; 20 GB MoE GGUF on 36 GB unified → 73k ctx with q4_0 KV works.
  - Validation: `go test ./internal/fit -run Calibration`

## Phase 4 — API (design-first)
- [x] 7. Extend `contracts/llama-skein.openapi.json`: `GET /api/fit`, `GET /api/fit/{model}`
  (`?ctx=`), `GET /api/catalog`; replace the `/api/models/context/{model}` stub schema.
  Regenerate Go: `go generate ./pkg/apicontract && gofmt -w pkg/apicontract/llama_skein.gen.go`.
  - Validation: `make check-codegen`
- [ ] 8. Implement the handlers using generated types, wired to `internal/fit`.
  - Validation: `go test ./internal/server -run Fit`
- [ ] 9. Regenerate the opencode TS client (`bun run build:llama-skein-client`) so the
  contract is available downstream; note the version bump for skein's `go get`.
  - Validation: TS client compiles in opencode

## Phase 5 — Verify + document
- [ ] 10. `go build ./... && go test -short ./...`; update ECOSYSTEM.md fork-extensions list
  with the fit endpoints; attribute llmfit (MIT) in NOTICE/ATTRIBUTION.
  - Validation: `make test-all`
