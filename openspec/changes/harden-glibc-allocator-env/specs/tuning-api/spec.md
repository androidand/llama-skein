# Spec delta: tuning-api (harden-glibc-allocator-env)

## ADDED

### glibc allocator environment for llama.cpp backends

- On a **Linux** host, when launching a `backend: llamacpp` model, the server
  MUST inject a set of glibc allocator environment variables into the model's
  process environment. The built-in default set is
  `MALLOC_MMAP_THRESHOLD_=65536` and `MALLOC_TRIM_THRESHOLD_=65536`, sourced
  from the top-level `backend_env.linux_glibc` map in the tuning database.
  Their purpose is to cap per-arena heap growth so freed pages return to the
  OS, preventing RSS creep / OOM on long-lived load/unload and RAM-offload
  workloads.
- This injection is **independent of GPU-profile matching** — it applies even
  when no per-gfx profile exists for the host — because the fix is
  glibc-generic, not architecture-specific.
- On **non-Linux** hosts (e.g. Apple Silicon / MLX) the server MUST NOT inject
  these vars; they are meaningless to a non-glibc allocator.
- The env is **recommended, not forced**, overridable at every layer:
  - a variable already present in a model's `env:` always wins (the server
    MUST NOT overwrite it);
  - `tuning.backend_env = false` disables the allocator-env injection while
    leaving GPU flag tuning active;
  - `tuning.enabled = false` disables all auto-injection, env included;
  - a user `tuning-profiles.yaml` may replace the `backend_env.linux_glibc`
    values.

### GET /api/tuning

- Response gains a read-only `backend_env` object (string→string): the
  effective allocator env the server injects on this host (empty/absent on
  non-Linux or when disabled), so operators can see what is applied.

### PATCH /api/tuning

- Accepts an additional `backend_env` boolean. `false` persists
  `tuning.backend_env: false` (disable allocator-env injection); `true` or a
  null/omitted field defers to the built-in default (enabled on Linux).
