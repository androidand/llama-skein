# Proposal: Proactive fit guard — never load a model that would OOM-crash the host

## Why

llama-skein loaded an MLX model too large for a 24GB Mac's unified memory and
**hard-crashed both Macs** (2026-07). The fit engine (`internal/fit`) already
computes whether a model fits — but only to *report* via `/api/fit`. Nothing
consulted it before a load, so an oversized model was launched and exhausted
unified memory, taking the host down.

The existing memory guard is *reactive* (it unloads under critical pressure)
and cannot prevent this: it explicitly won't kill the currently-loading model,
and a single oversized load blows past unified memory faster than it can react.
On a Mac an OOM isn't a failed process — it's a dead machine.

The trigger this time was a nightly fleet auto-deploy restarting the hosts
(now disabled), but the defense must live in llama-skein: **any** load request
(a restart's preload, a concurrent agent, opencode) must not be able to crash
the host.

## What

A two-part **fit guard**, both halves fail-open (never act on a fit verdict
they cannot compute confidently — VRAM telemetry warming up, un-parseable
weights, non-modeled backend):

- **Proactive clamp (at startup / config load):** before the router captures
  per-model configs and before preload runs, consult the fit engine for every
  model. If a model won't fit at its configured context: shrink `--ctx-size`
  to the largest safe context ("refuse + shrink first"); or, if even a minimal
  context won't fit (weights exceed memory), record it as unfittable. This
  also protects **preload**, which bypasses the HTTP layer.
- **Refuse gate (per request):** a middleware that, before a *not-yet-loaded*
  model is dispatched to the router, refuses with `507` when the model can't
  fit — so the backend is never launched and the host never OOMs. A model
  already resident fit before, so it is not re-gated.

`preload` skips unfittable models. A "confident won't-fit" is `FitLevel:"no"`
backed by a known host-VRAM figure and a known weight size; anything else is
treated as "don't know" and allowed through.

## Constraints

- Reuse the existing fit engine (`fitForModel`, unified-memory-aware via
  `vramMB`) — do not re-derive memory math.
- Fail open everywhere: the guard must never make the fleet *less* usable by
  refusing models it cannot size. Worst case it does nothing.
- No wire-contract change (the fit engine's `GpuSnapshot`/`ModelFit` DTOs are
  unchanged; the guard is internal behavior + a `507` on the existing routes).

## Non-goals

- Replacing the reactive memory guard (kept as a second line of defense).
- Live re-sizing of a running model's context (clamp is at load time).
- Fixing the nightly auto-deploy (disabled separately at the launchd layer).
