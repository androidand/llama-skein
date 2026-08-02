# Proposal: Preempt, don't fail, when a model switch waits on a busy model

## Why

A user switched models while their previous session had a stuck in-flight
generation on the loaded model (2026-08-01). llama-skein waited for the busy
model to drain, timed out, and failed the switch with "Timed out waiting for a
busy model to become available" — the user's own runaway request held their
fleet hostage, and the error named neither the culprit nor a way out.

The swap-wait default protects in-flight work, which is right for a shared
server but wrong as a dead end: an explicit model-switch request is a human
decision and must eventually win.

## STATUS (2026-08-02)

**Shipped**: the timeout error now names the blocking model(s), how many
requests are in flight there, and how long this request waited —
`internal/router/base.go`'s `expireStaleQueued`/`swapQueueTimeoutError` (was
already naming the model and wait time; in-flight count is new). Diagnoses
"busy, try later" vs. "something is stuck" without a separate `/health` call.

**Deferred — actual preemption is bigger than this proposal assumed.**
Investigating the swap code turned up two parallel implementations in this
repo: `internal/router` + `internal/process` (what `llama-skein.go`'s `main`
actually builds — confirmed via import graph) and the legacy `proxy/`
package (imported only by `cmd/legacy/llama-skein.go`, and the source of the
`cancelBusySlots()` this proposal assumed we'd "wire up"). **The live
implementation has no slot-cancel / mid-request-cancellation capability at
all** — `internal/process.Process` only exposes `Run`/`WaitReady`/`Stop`
(whole-process), nothing that interrupts one in-flight HTTP request without
killing the process. Building that is real, new work (a new interface
method; an implementation that can interrupt a live reverse-proxied request;
correct interaction with the run() loop's in-flight bookkeeping), not a
wiring task, and the existing "never interrupt a busy process" design was a
deliberate, documented choice (`base.go`: "killing an in-flight generation to
load something else would be worse") — worth respecting with fresh eyes and
its own review, not bolted on at the tail of an unrelated session.

## What changes (remaining)

- Add a mid-request cancellation primitive to `internal/process.Process`
  (interface + implementation) that can interrupt one in-flight `ServeHTTP`
  call without stopping the process — this is the actual prerequisite,
  not "call the existing slot-cancel path" (there isn't one on the live
  stack).
- When a swap blocks on a busy model past the queue timeout, **preempt**:
  cancel the in-flight request(s) via that new primitive, then proceed with
  the switch. The cancelled request's client receives a clear error
  ("preempted by model switch").
- `swapPolicy: preempt` is opt-in per host (default stays today's
  fail-after-timeout, matching the codebase's own documented reasoning and
  not silently changing behavior for other llama-skein deployments) — same
  warn/opt-in-never-force principle as the ctx ceiling and flash-attn work.

## Non-goals

- No priority system between models; requests are preempted oldest-first.
- No changes to `cmd/legacy` / `proxy/` — confirmed not the live path.
