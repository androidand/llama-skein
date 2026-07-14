# Proposal: Prevent and recover llama.cpp GPU-kernel wedges on the live path

## Why

z4's `--parallel 1` llama.cpp model wedges repeatedly — GPU pinned at 100%,
memory controller idle, zero tokens, and opencode hangs with **no feedback**.
The trigger is two requests racing into the single slot (observed: two parallel
opencode sessions). Root findings on the LIVE serving path (`internal/process`,
`internal/server` — NOT the dead `proxy` package an earlier fix wrongly
targeted):

- The serve path serializes **MLX** (`mlxSlot`) but **not llamacpp**, so
  concurrent requests race into a `--parallel 1` backend and deadlock it.
- There is **no request-duration cap** enforced, so a wedged request hangs
  indefinitely and the client gets no error.
- The inference probe *does* restart a wedged backend — but only when
  `inflight == 0`. A wedge that happens *during* an in-flight request keeps
  `inflight` at 1, so the probe never runs and the wedge is invisible.

## What

1. **Prevent the trigger — serialize llamacpp to its slot count.** Generalize
   the per-process serialization slot from MLX-only to any backend: capacity 1
   for MLX (unchanged), the explicit `--parallel`/`-np` value for llamacpp, and
   nil (unbounded, current behavior) for llamacpp without an explicit
   `--parallel`. Requests beyond capacity queue (honoring client disconnect),
   as MLX already does — they no longer race into the slot.
2. **Feedback + recovery backstop — enforce `maxRequestTimeSecs`.** In
   `ServeHTTP`, when the per-model cap (or the inherited global default) is set,
   bound the upstream request with a deadline. On expiry the client gets an
   error instead of an infinite hang, and the guard triggers recovery.
3. **Deterministic wedge recovery.** On the timeout (and the existing
   client-disconnect) path, run `cancelBusySlots`, enhanced to verify the slot
   actually released after the cancel and to `Stop()` the backend when it did
   not (the wedge signature: cancel ignored, slot still processing) — so the
   next request reloads a fresh process rather than hanging on a wedged one.

## Constraints

- Live path only (`internal/process`, `internal/server`). The `proxy` package
  is dead; do not touch it.
- Queue (don't 429) beyond the slot count, matching the existing MLX behavior.
- Config plumbing (`maxRequestTimeSecs` global + per-model inheritance) already
  exists in `internal/config`; only enforcement was missing.

## Non-goals

- A GPU-memory-activity watchdog that detects a wedge *during* an in-flight
  request without a wall-clock timeout (follow-up; prevention + the timeout
  backstop cover the reported trigger).
- Re-homing the other dead-`proxy` safeguards beyond what's needed here.
