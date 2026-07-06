# Tasks: harden-hardware-and-reload-api

- [x] 1. Deterministic `loadedModelInfo` (`internal/server/apihardware.go` +
       `apihardware_test.go`): collect all running models with resolvable
       files, return the one with the largest `model_mb` (tie-break: smallest
       ID). Test: two running models of different sizes (existing
       `newStubRouter` + `running` map scaffolding), assert the larger is
       always chosen across repeated calls.
       Validation: `go test -run TestServer_Hardware ./internal/server/`

- [x] 2. `NewCoalescingRunner` in new `internal/server/reloadrunner.go`
       (≤60 lines) + `reloadrunner_test.go`: trigger-during-run marks dirty
       and re-runs after the current pass; N triggers during one pass still
       cause exactly one follow-up; a trigger during the follow-up causes a
       third run (loop-until-clean); sequential triggers run sequentially.
       Wire in `llama-skein.go`: wrap `reload` with the runner and route ALL
       FOUR call sites through the wrapped func — `newSrv.SetReloadFn`
       (line ~233, inside the closure), `initialSrv.SetReloadFn` (~255),
       watcher `OnChange` (~272), SIGHUP handler (~303). The wrapped runner
       must be pre-declared with `var` (self-reference, same trick as
       documented at lines 187–191). Keep the closure's internal `reloading`
       guard as harmless redundancy.
       Validation: `go test -run TestCoalescingRunner ./internal/server/`

- [x] 3. `parseGGUFCached` on `Server` (`internal/server/apifit.go` + cache
       field in `server.go` + test in `apifit_test.go`): (path, mtime)-keyed
       cache in front of `gguf.ParseFile`; `fitForModel` uses it. Test
       `TestServer_Fit_GGUFCacheByMtime`: write a tiny valid GGUF, parse via
       `parseGGUFCached`, rewrite the file with different content restoring
       the original mtime via `os.Chtimes`, parse again → cached (stale)
       result returned; bump mtime → re-parse observed.
       Validation: `go test -run TestServer_Fit ./internal/server/`

- [x] 4. Slot-aware effective context (`internal/server/apifit.go` +
       `internal/fit/fit.go` + tests): parse `--parallel`/`-np` from the model
       cmd; effective hard ctx = configured ctx (or trained fallback) divided
       by slot count. `fit.Params` gains `ParallelSlots int` (0/1 = no
       division). Do NOT assume the trained context when `--ctx-size` is
       absent is reliable — observed llama-server b-a410713 defaults n_ctx
       4096 (1 slot) / 262144-split-by-4 depending on parallel; design D4
       documents why explicit config is policy. Test: cmd with `--parallel 4
       --ctx-size 262144` → max_safe_ctx computed from 65536.
       Validation: `go test ./internal/fit/ ./internal/server/ -run 'Fit'`

- [x] 5. Explicit ctx policy in the Docker config
       (`config.docker-default.yaml` + `docker-entrypoint.sh` comment):
       add `--parallel 1` to the default model cmd; replace the false
       "llama-server defaults to each model's own trained context length"
       comment with the observed behavior and recommend explicit
       `--ctx-size`/`LLAMA_ARG_CTX_SIZE`. (Live z4 already patched manually
       to `--parallel 1 --ctx-size 131072` on 2026-07-06.)
       Validation: `grep -A6 'cmd:' config.docker-default.yaml`

- [x] 6. Contract description updates + regen (batched):
       `contracts/llama-skein.openapi.json` — `LoadedModelInfo` selection
       rule sentence; `ModelFit.max_safe_ctx` note that slot division is
       accounted for. `go generate ./pkg/apicontract` + gofmt.
       Validation: `make check-codegen`

- [x] 7. Repo validation: `gofmt -w` touched files, `go build ./...`,
       `go test -short ./...`.
