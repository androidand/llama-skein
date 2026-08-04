# Tasks

- [x] Define model descriptor type (params, arch family, quant variants with file sizes, requested ctx) in `internal/fit`
      — `internal/fit/descriptor.go`: `Descriptor` struct + `ShapeFromDescriptor`.
- [x] Descriptor-based scoring entry point reusing existing llama.cpp fit math; estimated-metadata fallback (layers/heads from params+arch) with `estimated: true` flag in the verdict
      — dense-preset table anchored to real checkpoints (qwen2.5-0.5b…llama3.1-70b); missing KV-head count assumes GQA-8, not full MHA. `Params.Unproven` added so a candidate that has never loaded doesn't get the deployed-model "no"→"marginal" rescue or the `UnderConfigured` flag.
- [x] ~~MLX path: score descriptor against unified-memory budget in `fit/mlx.go`~~ — CORRECTED: no `mlx.go` changes needed.
      `mlx.go`'s only backend-specific logic (`ShapeFromMLXConfig`) parses a real `config.json`, which a not-yet-downloaded candidate doesn't have. `ShapeFromDescriptor` produces a backend-neutral `ModelShape` either way, and `s.vramMB()` already returns the correct unified-memory budget for Apple Silicon (`hostVRAM`'s existing `unified` branch) — MLX is scored through the exact same descriptor path as llama.cpp, just without cache-type KV quantization (defaults to f16, matching real MLX behavior).
- [x] `POST /api/fit/hypothetical`: multi-variant request → per-variant verdicts, max safe ctx, recommended variant; same vocabulary as `/api/fit`
      — `internal/server/apifit_hypothetical.go` + route registration in `server.go`.
- [x] Unit tests: known GGUF models scored via descriptor match their file-based scores within tolerance; estimated path sane for common arch families
      — `internal/fit/descriptor_test.go`: explicit-dims descriptor matches the GGUF path exactly (not just "within tolerance" — same engine, same inputs); estimation sane across 0.5B–150B; GQA default; both `Unproven` guards regression-tested.
- [x] Update `contracts/llama-skein.openapi.json` + regenerate clients
      — Found and fixed a real bug along the way: the new `HypotheticalVariantFit.fit_level` enum shared value strings with the existing `ModelFit.fit_level`, which made oapi-codegen's collision-avoidance rename ALL fit-level constants repo-wide (`Unknown`→`ModelFitFitLevelUnknown` etc.), breaking `apifit.go` and `fitguard_test.go`. Fixed properly: extracted a shared `FitLevel` schema, both properties `$ref` it, both generated fields are now `apicontract.FitLevel`. Updated the two call sites that used the old generated name.
- [ ] Live smoke test on one CUDA/ROCm host and one MLX host
      — not run: this session has no live host access. Needs a manual pass before merge.

## Known blocker (unrelated to this change)

`internal/server` does not build on `main` independent of this work: `server.go`
references `ProfileStore`/`NewProfileStore` and four runtime-backend handlers
(`handleListRuntimes`, `handleInstallRuntime`, `handleUpgradeRuntime`,
`handleCheckRuntimeHealth`) that have never existed in ANY commit, branch, or
stash in this repository's history (checked all ~30 local/remote branches).
`backup/27b8f95-original`'s `openspec/changes/add-persistent-user-profile-saving/.skein/blocked-reason.md`
shows why: `NO_PROGRESS_REPEATED` recorded 2026-06-02 — the skein supervisor's
coder loop got stuck and never produced the implementation, yet `tasks.md` for
both `add-persistent-user-profile-saving` and `add-backend-runtime-management`
shows every task checked, and the dangling references were committed to `main`
regardless. This is the hollow-progress failure mode from the
`fleet-control-plane-rescope` post-mortem, caught live.

This change was verified in isolation (a throwaway stub for the missing
symbols, deleted before commit, confirmed `internal/fit` + `internal/server`
build and 244 tests pass) rather than fixed, since implementing `ProfileStore`
and runtime-backend management is a separate, unscoped feature — guessing its
shape would repeat the exact mistake being diagnosed. `main` needs its own fix
before `go build ./...` is green again.
