# Spec: Verified model readiness

## Changes

- CHANGED: for non-llamacpp backends (mlx, vllm), `StateReady` is reached only
  after a warm-up inference succeeds. Previously `doStart` logged a warning on
  warm-up failure and returned success, marking the model `ready` while the
  upstream process was alive but the model not resident. Now a warm-up failure
  aborts the start (the up-but-not-resident process is killed) and `Run` returns
  an error — `ready` means resident and serving, for every backend.

- CHANGED: repeated warm-up (verified-readiness) failures count toward the
  crash-loop breaker, so a model that comes up but cannot serve surfaces the
  existing "crashed N times, refusing restart" error to clients instead of a
  silent start+fail cycle on every request.

- CONFIRMED (no behaviour change, covered by test): the periodic inference probe
  drives a post-ready wedge (model evicted or generation thread dead while the
  process lives) to `stopped`, and the `mlxSlot` serialization never blocks a
  real request indefinitely on a stuck probe or generation.

## Behaviour notes

- llama.cpp is unaffected: its `/health` already reflects a loaded model, so its
  readiness path is unchanged (no warm-up gate).
- The control-plane contract is restored: `/running` and `/v1/models`
  `state: ready` mean the model will serve the next request, so skein and
  opencode no longer trust a stale `ready`.
- A failed start returns a real error to the caller (and, on repeat, the
  crash-loop error) — strictly better than a `ready` that cold-loads for 15s or
  hangs for 30s on the next request.
- This addresses *is the model serving*; the separate "what is its real context
  window" truth (the 84k mismatch) and the fit engine are tracked under
  skein-uyyu.
