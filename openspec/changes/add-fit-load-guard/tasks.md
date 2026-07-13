# Tasks: add-fit-load-guard

- [x] 1. `internal/server/fitguard.go`: `setCtxSizeInCmd`, `confidentNoFit`,
       `clampModelsToFit` (shrink or flag unfittable, fail-open),
       `modelLoadRefusal`, `CreateLoadFitGateMiddleware` (507).
- [x] 2. `Server` gains `unfittable map[string]string` (populated in New,
       read-only after).
- [x] 3. Reorder `New`: build Server (cfg+perf) → `clampModelsToFit` → build
       routers from the clamped `s.cfg`.
- [x] 4. Wire `CreateLoadFitGateMiddleware` into the model chain (after the
       concurrency guard, before the prompt guard).
- [x] 5. `startPreload` skips models `modelLoadRefusal` rejects.
- [x] 6. Tests: `setCtxSizeInCmd`, `confidentNoFit`, `modelLoadRefusal`.
- [x] 7. `go build ./...`, `go test -short ./proxy/... ./internal/...` (982 ok).
- [ ] 8. Deploy to m5 (verify it does NOT over-refuse fittable models — load a
       small model successfully; check /api/fit + fit-guard logs), then m3.
- [ ] 9. Follow-up (separate): the reactive guard's macOS calibration
       (`mlx-macos-gotchas`) and re-home the GPU-wedge safeguards onto the
       live `internal/process` path (the `proxy` package is dead code).
