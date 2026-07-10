# Proposal: Inject glibc allocator env caps for llama.cpp backends on Linux

## Why

On Linux/glibc, a long-lived server that repeatedly loads and frees large
buffers — exactly what `llama-server` does when llama-skein swaps models on a
group with `autoUnload`, or offloads MoE experts to system RAM — suffers RSS
creep: glibc's per-arena heap fragments and never returns freed pages to the
OS, so resident memory grows for hours until Linux OOM-kills the process. This
hits AMD/ROCm rigs hardest because consumer VRAM is small, so we offload to
system RAM, which drives far more allocator churn than a big-VRAM box that
keeps everything on-card.

The fix is two glibc tunables set before the process starts:

```
MALLOC_MMAP_THRESHOLD_=65536
MALLOC_TRIM_THRESHOLD_=65536
```

They force allocations ≥64 KiB through `mmap` (returned to the OS on free) and
trim the main arena aggressively. A widely-reported result: 13 diffusion
models cycling on a 7800 XT went from OOM at 52 GB after 17 h to a stable
~1.2 GB indefinitely (r/ROCm, github.com/brjen/pytorch-memory-fix).

llama-skein is well placed to apply this automatically: it owns the
`llama-server` child's environment (`proxy/process.go` builds `cmd.Env`), and
its tuning subsystem already injects "recommended, never forced" defaults per
host. This is the natural home.

## What

- The tuning subsystem injects the two `MALLOC_*` vars into every
  `backend: llamacpp` model's environment on **Linux** hosts, as overridable
  defaults sourced from a new top-level `backend_env.linux_glibc` block in the
  tuning database (`internal/tuning/profiles.yaml`).
- Injection is independent of GPU-profile matching (the fix is glibc-generic,
  not gfx-specific) but respects the same override layers: a var already set
  in a model's `env:` wins; `tuning.backend_env: false` disables just the env
  injection; `tuning.enabled: false` disables all auto-injection; a user
  `tuning-profiles.yaml` can override the values.
- On non-Linux hosts (m5 / Apple / MLX) it is a no-op — the vars mean nothing
  to Apple's allocator.
- `GET /api/tuning` reports the effective `backend_env` map; `PATCH
  /api/tuning` accepts a `backend_env` boolean to toggle it.
- `docker-entrypoint.sh` also exports the two vars (mirroring its existing
  PATH export) so containers get the protection immediately, even before the
  new binary's injection logic and even for tuning-disabled setups.

## Constraints

- `contracts/llama-skein.openapi.json` is source of truth — spec first, then
  `go generate ./pkg/apicontract`, then handlers. Regenerate the opencode TS
  client after.
- Defaults, never forced (matches the existing tuning philosophy): every layer
  above is overridable/disable-able.
- Values are strings in YAML/env; the trailing underscore in the var names is
  the required glibc secure-env form.

## Non-goals

- Tuning `MALLOC_ARENA_MAX` or other allocator knobs — scope is the two vars
  in the source PSA, which are measured; others can be added later via the
  same `backend_env` mechanism or per-model `env:`.
- Applying to MLX/vLLM backends or non-Linux hosts.
- Changing the value per gfx — it is a single Linux-wide default.
