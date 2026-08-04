# Tasks

- [x] 1.1 Define model descriptor type (params, arch family, quant variants with file sizes, requested ctx) in `internal/fit`
      — `internal/fit/descriptor.go`: `Descriptor` struct + `ShapeFromDescriptor`.
- [x] 1.2 Descriptor-based scoring entry point reusing existing llama.cpp fit math; estimated-metadata fallback (layers/heads from params+arch) with `estimated: true` flag in the verdict
      — dense-preset table anchored to real checkpoints (qwen2.5-0.5b…llama3.1-70b); missing KV-head count assumes GQA-8, not full MHA. `Params.Unproven` added so a candidate that has never loaded doesn't get the deployed-model "no"→"marginal" rescue or the `UnderConfigured` flag.
- [x] 1.3 ~~MLX path: score descriptor against unified-memory budget in `fit/mlx.go`~~ — CORRECTED: no `mlx.go` changes needed.
      `mlx.go`'s only backend-specific logic (`ShapeFromMLXConfig`) parses a real `config.json`, which a not-yet-downloaded candidate doesn't have. `ShapeFromDescriptor` produces a backend-neutral `ModelShape` either way, and `s.vramMB()` already returns the correct unified-memory budget for Apple Silicon (`hostVRAM`'s existing `unified` branch) — MLX is scored through the exact same descriptor path as llama.cpp, just without cache-type KV quantization (defaults to f16, matching real MLX behavior).
- [x] 1.4 `POST /api/fit/hypothetical`: multi-variant request → per-variant verdicts, max safe ctx, recommended variant; same vocabulary as `/api/fit`
      — `internal/server/apifit_hypothetical.go` + route registration in `server.go`.
- [x] 1.5 Unit tests: known GGUF models scored via descriptor match their file-based scores within tolerance; estimated path sane for common arch families
      — `internal/fit/descriptor_test.go`: explicit-dims descriptor matches the GGUF path exactly (not just "within tolerance" — same engine, same inputs); estimation sane across 0.5B–150B; GQA default; both `Unproven` guards regression-tested.
- [x] 1.6 Update `contracts/llama-skein.openapi.json` + regenerate clients
      — Found and fixed a real bug along the way: the new `HypotheticalVariantFit.fit_level` enum shared value strings with the existing `ModelFit.fit_level`, which made oapi-codegen's collision-avoidance rename ALL fit-level constants repo-wide (`Unknown`→`ModelFitFitLevelUnknown` etc.), breaking `apifit.go` and `fitguard_test.go`. Fixed properly: extracted a shared `FitLevel` schema, both properties `$ref` it, both generated fields are now `apicontract.FitLevel`. Updated the two call sites that used the old generated name.
- [x] 1.7 Live smoke test on one CUDA/ROCm host and one MLX host
      — MLX leg DONE 2026-08-04 on m3 (Apple M3 Pro, 36864MB unified memory,
      real host — user confirmed safe to use since it wasn't otherwise in
      service). Merged current `main` first (see below), built the binary
      standalone, ran it on a scratch port with an empty scratch config
      (`models: {}`) so the live `com.llamaswap.m3` launchd job at :11435 was
      never touched — `/api/fit/hypothetical` scores a candidate against host
      capacity, it does not need any model actually configured. Hardware
      polling correctly detected the real GPU/unified-memory numbers (cross-
      checked against the live service's `/api/hardware`, one read-only GET).
      Called both backends against real, currently memory-pressured host state
      (the live service had a model loaded, swap ~97% full): `llamacpp`
      dense-32B-estimated returned `Q4_K_M` "good"/`Q8_0` "no" with sane
      vram/ctx numbers and `recommended: "Q4_K_M"`; `mlx` dense-8B-estimated
      returned both variants "perfect" with `recommended: "8bit"`. Malformed
      requests (`variants: []`, unsupported backend) both returned clean 400s
      with the documented error strings. No crashes, no NaN/negative fields.
      Scratch process killed and scratch files removed afterward; live service
      health-checked 200 before and after.
      CUDA/ROCm leg DONE 2026-08-04 on rocky (AMD Radeon RX 7900 XTX, gfx1100,
      24560MB VRAM — user powered it on and confirmed it was idling and safe
      to use; first attempt earlier the same day found it unreachable —
      ssh/ping both down, no ARP entry, no WOL configured — resolved once the
      user turned it on). Cross-compiled `GOOS=linux GOARCH=amd64`, scp'd to
      `/tmp` on rocky, ran standalone on scratch port 18099 with the same
      empty `models: {}` scratch config pattern as m3 — the live
      `llama-skein.service` (systemd --user unit, port 11435) was never
      stopped or restarted; confirmed active and health-checked 200 both
      before and after. Hardware polling matched the live service's real
      `/api/hardware` GPU/VRAM numbers exactly (24560MB total, 226MB used,
      idle). `llamacpp` dense-32B-estimated returned `Q4_K_M` "tight"/`Q8_0`
      "no" (Rocky's real 24GB budget makes the same descriptor tighter than
      m3's 36GB unified memory did — expected, not a bug); dense-8B-estimated
      returned both variants "perfect". Both malformed-request cases returned
      clean 400s with the documented error strings. Scratch process and files
      removed afterward (`pgrep` confirmed no leftover process, `ls`
      confirmed no leftover files).
      proxmox/z4 remain untried — not required by this task (one CUDA/ROCm
      host + one MLX host, both now done) and both are live/production, so
      left alone unless separately requested.

## Known blocker (unrelated to this change) — RESOLVED as of this merge

`internal/server` used to fail to build on `main` independent of this work
(`ProfileStore`/`NewProfileStore` and four runtime-backend handlers referenced
but never implemented — the hollow-progress failure mode from the
`fleet-control-plane-rescope` post-mortem). As of merging `main` @ `21f022b`
into this branch (2026-08-04), `internal/server/apiprofile.go` and
`internal/server/apiruntime.go` now contain real implementations and the
merged worktree builds and passes `go test ./...` (`internal/fit`,
`internal/server`, and the rest) with no stub required. `main` fixed itself
between when this note was written and now; no action needed here.
