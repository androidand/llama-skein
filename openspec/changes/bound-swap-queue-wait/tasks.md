# Tasks: bound-swap-queue-wait

- [x] 1. Config: `SwapQueueTimeoutSecs int` on `config.Config`, default 10 in
       `LoadConfigFromReader`. Fixed the two exact-struct-equality config
       tests (posix + windows) that needed the new default added.
- [x] 2. `internal/router`: `ErrSwapQueueTimeout` sentinel + `SendError` case
       (503); `swapQueueTimeoutError` builder naming the blocking model(s).
- [x] 3. `handlerReq` gains `queuedAt time.Time`, stamped at both queue sites
       in `handleRequest` (collision + would-evict-busy).
- [x] 4. `expireStaleQueued(now, timeout, active, &queued)` — pure, takes
       `now` as a parameter (not `time.Now()`) so it's directly unit-testable
       without real sleeps. Grants an error + removes expired entries;
       preserves order and untouched entries otherwise.
- [x] 5. Wire a ticker into `run()` (`queueScanInterval`, default 1s,
       overridable for tests) calling `expireStaleQueued`. Does not call
       `notifyProcessed()` (time-driven, not request-driven — same reasoning
       as the existing `serveDoneCh` case).
- [x] 6. Tests: `expireStaleQueued` partition/error-content/zero-disables/
       empty-queue (pure unit), plus one true end-to-end test driving the
       real `run()` loop with a wedged (`serveBlock`) process to prove the
       wiring, not just the isolated function.
- [x] 7. `go build ./...`, `go test -short ./proxy/... ./internal/...`
       (992 ok), `docs/`/`config.docker-default.yaml` note.
- [ ] 8. Deploy to the fleet; verify against a real forced wedge that a
       competing model request now gets a 503 within ~10s instead of hanging.
