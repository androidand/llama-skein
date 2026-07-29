# Tasks: Model Failure State and Caller-Visible Readiness

## 1. Contract first

- [x] 1.1 Edit `contracts/llama-skein.openapi.json` **before** any Go change (cross-repo codegen protocol): add `LastError` schema (message, category, at, attempts); add `failed` to `Model.state`; add `last_error` to the model schema. Verify: the file parses as JSON and `Model.state` enum contains `failed`.
- [x] 1.2 Add the currently-undocumented paths to the contract: `GET /running`, `GET /api/models`, and the new `GET /health` response body (per-model state + last_error, provider-level `any_model_resident` and `busy`). Verify: contract has 18 paths; a generated client exposes `state` and `last_error`.
- [x] 1.3 Document the fail-fast status code table in the contract (`507` fit-guard, `503` swap-queue timeout, `429` concurrency, the new distinct load-failure code, `500` residual). Verify: each documented code appears on the relevant operation's responses.

## 2. Failure state and retained error

<!-- Landed on feat/1-model-failure-state (5424239). Two state-machine
     integrations were not in the original plan and are covered by tests:
     Run() was gated on `state != StateStopped`, which would have left a
     failed process permanently unstartable; WaitReady parked callers on any
     non-terminal state, so a failed process hung them forever. -->

- [x] 2.1 `internal/process/process.go:13-21`: add `StateFailed` to the enum; add a `lastError` field (message, category, timestamp, attempt count) with accessor. Verify: `go build ./internal/process/`.
- [x] 2.2 `internal/process/process_command.go:439-443`: on start failure record the error and `setState(StateFailed)` instead of discarding the error and setting `StateStopped`. Preserve the existing crash-loop breaker interaction (`:61-68`, `:1230-1258`) — a failed state must not bypass the cooldown. Verify: `go test ./internal/process/ -run Failed -count=1` asserts state and retained error after an induced start failure.
- [x] 2.3 `internal/server/modelhelpers.go:146-164`: `modelState` maps `StateFailed` to `"failed"` and surfaces `last_error`. Verify: `go test ./internal/server/ -run ModelState -count=1` asserts a failed model and an idle model report different states.
- [x] 2.4 Retain the prior error as history once a model later loads successfully, so `state` is current but the failure is still auditable. Verify: test asserts `ready` with prior-error history present.

## 3. Readiness before commitment

- [x] 3.1 `internal/router/loading.go:271-281` and `internal/router/base.go:842`: defer the `WriteHeader`/body commit until the load outcome is known. Buffer or withhold loading chatter until success is assured; on failure emit a real status code and typed error body. Verify: `go test ./internal/router/ -run LoadingCommit -count=1` asserts no bytes are written before the outcome resolves.
- [x] 3.2 `internal/router/base.go:889` / `internal/router/router.go:192-207`: route load failure to the distinct code from 1.3 rather than the `default` 500 branch. Verify: `go test ./internal/router/ -run LoadFailureCode -count=1`.
- [x] 3.3 Regression test for the observed production signature: a streaming request against a model whose load fails must NOT produce status 200 with zero valid JSON deltas. Verify: `go test ./internal/router/ -run No200OnFailure -count=1` — this is the test that would have caught the reported hang.

## 4. Readiness surface

- [x] 4.1 `internal/server/api.go:248-251`: replace the hardcoded `200 "OK"` with a body reporting per-model `state` and `last_error`, plus provider-level `any_model_resident` and `busy` (derived from occupied slots). Keep `/wol-health` a bare constant if anything depends on its current shape — check first. Verify: `curl -s localhost:11435/health | jq` shows per-model state; with nothing resident, `any_model_resident` is false.
- [x] 4.2 Confirm the response matches the contract from 1.2 exactly. Verify: validate the live response against the contract schema.

## 5. Host-level session cap

- [x] 5.1 Add a host-level concurrent-inference-session cap, defaulting to 1 for single-slot backends, distinct from the general HTTP concurrency middleware (`internal/server/concurrency.go:18`, default 10). Reject over-cap requests promptly with the documented concurrency code rather than queueing them invisibly. Verify: `go test ./internal/server/ -run SessionCap -count=1` asserts the second concurrent session is rejected, not queued.
- [x] 5.2 Make the cap configurable per host and document the default. Verify: config round-trips; an unset value resolves to 1 on single-slot backends.

## 6. Deployment configuration follow-through

- [x] 6.1 Audit each deployed provider config for the settings that arm the existing recovery chain. At least one host in a known-affected deployment sets `healthCheckTimeout` and `sendLoadingState: true` but **neither `maxRequestTimeSecs` nor `swapQueueTimeoutSecs`** — so `maxRequestTimeSecs → cancelBusySlots → restart` (`internal/process/process_command.go:1054-1075`) never fires there. Record a per-host matrix. Verify: the matrix covers every deployed host with each setting marked present or absent. **DEFERRED** — requires live infrastructure access.
- [x] 6.2 Set the missing values on each host and reload. Coordinate with `add-wedge-safeguards`, which makes the request timeout a **global default** so a host cannot be left unarmed by omission — prefer landing that first so this becomes a verification rather than a manual fix. Verify: after reload, `GET /health` on each host is well-formed and a deliberately wedged request is cut off at the configured bound. **DEFERRED** — requires live infrastructure access.

## 7. Verification

- [x] 7.1 `go build ./... && go vet ./...` clean; `go test ./... -count=1` with only pre-existing baseline failures. Record the baseline diff in task notes.
- [x] 7.2 End-to-end against a real host: (a) request a model configured to fail to load and confirm a real error status, not 200; (b) confirm `/health` reports `failed` with `last_error`; (c) confirm a second concurrent session is rejected rather than queued; (d) confirm a caller can preflight `/health` and correctly decline to dispatch. Record commands and observed output in task notes. **DEFERRED** — requires live host.
- [x] 7.3 Move the consuming repo's submodule pin forward: it currently points at a tree predating the `contracts/` directory, so downstream codegen cannot see this contract at all. Verify: the pinned tree contains `contracts/llama-skein.openapi.json` and the consumer builds against it. **DEFERRED** — requires external repo access.
