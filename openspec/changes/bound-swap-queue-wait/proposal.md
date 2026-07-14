# Proposal: Bound how long a model-switch request waits behind a busy sibling

## Why

When two opencode sessions used z4 for different models, switching to the
second model appeared to hang. Root cause traced to the router's queueing
logic, not a bug in it: `baseRouter.handleRequest` correctly refuses to evict
a model with an in-flight request — case (5) queues the competing request
instead of interrupting a busy generation, which is the right call. But that
queue has no timeout: `drainQueue` only re-runs when the busy model's request
completes (`serveDoneCh`). If that model is wedged (GPU-kernel deadlock, not
merely slow), its request never completes, `inFlight` never drops, and the
queued request for the other model waits with **zero bound** — not even the
existing `maxRequestTimeSecs`, since that only bounds a request already
dispatched to a process; a request parked in the router's queue never reaches
that far.

The user's own read on it ("clients battling over which model to load")
pointed at the right neighborhood: the fix isn't refusing switches outright
(the router already avoids the dangerous case — interrupting a busy
generation) — it's giving the caller a bounded wait and a clear error instead
of an indefinite hang when what it's queued behind doesn't drain in time.

## What

- New config `swapQueueTimeoutSecs` (global, default 10s, 0 disables):
  bounds how long a request may sit queued behind a busy sibling model that
  must be evicted first.
- A periodic scan (`expireStaleQueued`, ticked from the router's existing
  `run()` loop) walks the queue; any entry queued longer than the bound is
  granted `ErrSwapQueueTimeout` (surfaced as `503`, naming the model it was
  blocked on) instead of left waiting.
- Applies uniformly to both queueing cases in `handleRequest` (collision with
  an in-flight swap, and would-evict-a-busy-process) — not just a
  wedge-specific safety net, per explicit choice: consistent behavior whether
  the sibling is genuinely busy or wedged.

## Constraints

- Must not touch the actual eviction decision — a busy process is still never
  interrupted. This only bounds the WAIT for a request that's already been
  correctly deferred.
- No wire-contract change (config.yaml field, not an API/schema field); the
  error response format follows the existing `SendError` sentinel-error
  convention (`ErrNoLocalModelFound`, etc.).
- The scan must not affect existing router tests, which build `config.Config`
  directly (not through `LoadConfigFromReader`) and so get the Go zero value
  (0 = disabled) unless a test opts in — confirmed via the full existing
  suite passing unchanged.

## Non-goals

- Detecting whether the blocking model is wedged vs. genuinely busy (the
  timeout is uniform, per the "Always" scope decision) — a follow-up could add
  a `/slots`-aware surgical variant for llama.cpp backends specifically.
- Retry/backoff behavior on the client (opencode) side — out of scope for
  this repo; the 503 + clear message gives it what it needs to do so.
