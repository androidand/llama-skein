# Proposal: Verified readiness for MLX/vLLM (residency, not /health)

## Context

llama-skein marks a model `StateReady` once its upstream process is up and
`GET /health` returns 200. For llama.cpp that is sound — `/health` reflects a
loaded model. For **MLX and vLLM it is a lie**: `mlx_lm.server` answers `/health`
the moment its HTTP server binds, *before* weights are resident, and keeps
answering 200 if the model is later evicted or its generation thread wedges.

`doStart` already sends a warm-up inference for non-llamacpp backends to force
eager loading — but a **warm-up failure is logged as a warning and the start
returns success anyway** (`process_command.go` ~L616), so the model is marked
`ready` even when warm-up proved it is not resident.

Confirmed live on M3 (2026-06-19): `/running` and `/v1/models` reported
`state: ready`; a fresh request took 14.8s (a cold load — the model was not
resident) and the next hung 30s. Killing the process correctly flipped state to
`stopped`, so true-exit detection works; the gap is **process-alive-but-not-serving**.
skein-69qu.

## Why

`ready` is a control-plane contract: skein routes to it, opencode shows it loaded,
the sidebar trusts it. A `ready` that isn't serving makes every downstream layer
lie to the user ("looks like responding but isn't"). Readiness must mean the
model is resident and answering inference — for every backend.

## What

- **Gate readiness on warm-up success** for non-llamacpp backends: if the warm-up
  inference fails, `doStart` aborts (kills the up-but-not-resident process and
  returns an error) instead of returning success. A model only reaches
  `StateReady` after a real inference has succeeded.
- **Keep + verify the periodic inference probe** (`inferenceProbeLoop`) that
  already exists to catch a *post-ready* wedge (model evicted / generation thread
  dead while the process lives): confirm it correctly drives the process to
  `stopped` so state stops lying, and review its interaction with the `mlxSlot`
  serialization (the 30s hang — a probe or stuck generation must not block real
  requests indefinitely).
- **Count verified-readiness failures toward the crash-loop breaker** so a model
  that repeatedly comes up but cannot serve surfaces the existing "crashed N times,
  refusing restart" error to clients instead of silently retrying every request.

## Scope

In: the readiness gate + probe hardening in `internal/process`, applied to all
non-llamacpp backends (mlx, vllm). Live-tested on M3 + M5 (both run MLX).

Out: the downstream context-limit truth (the 84k mismatch) and the fit engine —
related "model truth" work tracked separately (skein-uyyu, and the context-mismatch
follow-up). This change is strictly about *is the model actually serving*.
