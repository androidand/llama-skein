# Model Failure State and Caller-Visible Readiness

## Why

A caller cannot tell a healthy idle provider from a broken one, and cannot tell a failed inference from a slow one. That single gap is the root of the recurring "the provider stopped loading its model and needs a manual process reload" symptom, and of the caller-side session hangs it causes.

Three defects compose:

1. **No failure state exists.** The process state enum is `stopped | starting | ready | stopping | shutdown` (`internal/process/process.go:13-21`). On a start failure, `internal/process/process_command.go:439-443` calls `setState(StateStopped)` and returns the error to its one caller; **the error is retained nowhere queryable**. `modelState` (`internal/server/modelhelpers.go:146-164`) therefore reports a model that just failed to load as `"stopped"` — byte-identical to a model that was never asked to load.

2. **The transport commits success before the outcome is known.** With `sendLoadingState: true`, any streaming `/v1/chat/completions` against a not-ready model starts a `loadingWriter` (`internal/router/base.go:842`) which writes **HTTP 200 plus an SSE body immediately** (`internal/router/loading.go:271-281`). If the load then fails, `base.go:889` calls `SendError`, whose `WriteHeader` is a no-op on an already-committed response — so the error JSON is appended into the stream instead of being a status code. The caller receives a success-shaped non-answer.

   The observable signature, reproduced repeatedly in production logs: **HTTP 200, a multi-minute duration, a six-figure byte count, and zero valid JSON deltas**, typically alongside a `cancelBusySlots: /slots failed — backend appears hung, restarting` entry and a client-side `error processing streaming response: no valid JSON data found in stream`.

3. **`/health` is a constant.** `internal/server/api.go:248-251` returns a hardcoded `200 "OK"` that says nothing about any model, and neither `/running` nor `/api/models` — the only endpoints exposing per-model `state` — is in `contracts/llama-skein.openapi.json`. A generated cross-repo client cannot read model state through the contract at all.

The downstream cost is concrete. An orchestrator sees a reachable provider, dispatches, and blocks; the caller's only backstop is a wall-clock timeout, which reads to a human as "hung forever". Recovery does not close the loop either: **every** recovery path in this codebase ends in "stopped; the next request will restart it", so if no request arrives, nothing reloads — which is exactly what a manual process reload works around.

This change makes failure expressible, observable, and contractual. It does **not** attempt to make loads never fail.

## What Changes

- **A `failed` state and a retained last error.** The state enum gains `failed`. Start failures record `last_error` (message, category, timestamp, attempt count) on the process and expose it wherever `state` is exposed. A failed model is no longer indistinguishable from an idle one.
- **Readiness before commitment.** When `sendLoadingState` is enabled, the response is not committed until the load either succeeds or fails. On failure the caller gets a real status code and a typed error body, never a 200 with an error buried in the stream. Loading progress is still streamable, but only once success is assured.
- **A pollable provider readiness surface.** `GET /health` gains a real body reporting per-model `state`, `last_error`, and whether any model is resident, plus a provider-level `busy` indicator derived from occupied slots. A caller can preflight this before dispatching.
- **Contract coverage.** `/running`, `/api/models`, and the extended `/health` are added to `contracts/llama-skein.openapi.json`, with `state` including `failed` and a `last_error` schema — so cross-repo codegen can consume readiness. Per the cross-repo protocol the contract is edited **first**.
- **A documented fail-fast code table.** The existing scattered codes (507 fit-guard, 503 swap-queue, 429 concurrency, 500 swap error) are documented as a contract, and load failure moves off the catch-all 500 (`internal/router/router.go:192-207`) onto a distinct code so callers can distinguish "this model will not load" from "something went wrong".
- **A single-session concurrency cap.** An explicit host-level limit on concurrent inference sessions, defaulting to 1 for single-slot backends. Today serialization is per-model at backend slot count (`internal/process/process_command.go:160-171`) while the HTTP concurrency middleware defaults to **10** (`internal/server/concurrency.go:18`) — so nothing enforces the "one session at a time" invariant that callers are expected to respect.

## Capabilities

### New Capabilities
- `model-failure-state`: the `failed` state and retained `last_error`; readiness-before-commitment on the streaming path; the pollable readiness surface; contract coverage for readiness; the documented fail-fast code table; the host-level session cap.

### Modified Capabilities
- `hardware-and-reload-api`: `/health` gains a body; `loaded_model` reporting is joined by explicit failure reporting.

## Non-Goals

- **Not** an automatic model reloader or supervisor. Closing the "nothing reloads a stopped model" loop is `add-wedge-safeguards` (global default request timeout, plus the Apple Silicon watchdog gap tracked there). This change makes the condition *visible*; that one makes it *self-heal*.
- **Not** a change to fit/OOM prevention — `add-fit-load-guard` already refuses OOM-inducing loads with 507.
- No change to the `/v1/*` OpenAI-compatible request schema; only status codes and error bodies on the failure path.

## Impact

- `internal/process/process.go` — enum gains `failed`; process gains `lastError`.
- `internal/process/process_command.go:439-443` — record the error instead of discarding it; set `failed`.
- `internal/server/modelhelpers.go:146-164` — `modelState` maps the new state and surfaces `last_error`.
- `internal/router/loading.go:271-281`, `internal/router/base.go:842,889` — defer response commitment until the load outcome is known.
- `internal/router/router.go:192-207` — distinct code for load failure; documented table.
- `internal/server/api.go:248-251` — `/health` body.
- `internal/server/concurrency.go:18` — host-level session cap, default 1 for single-slot backends.
- `contracts/llama-skein.openapi.json` — `/running`, `/api/models`, `/health`; `Model.state` gains `failed`; new `LastError` schema. **Edited first**, per the cross-repo codegen protocol.
- Downstream consumers gain a real readiness signal: the orchestrator's provider client, and the sub-agent placement probe in the companion harness, which currently infers readiness from reachability alone.
