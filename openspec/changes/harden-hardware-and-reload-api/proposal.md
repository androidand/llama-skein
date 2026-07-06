# Proposal: Harden /api/hardware and config reload

## Why

Code review (2026-07-05, post kv_estimate_mb fallback) verified three defects
in the control-plane server:

1. **Nondeterministic `loaded_model`.** `loadedModelInfo()`
   (`internal/server/apihardware.go:31`) iterates `s.local.RunningModels()` —
   a Go map — and returns the first entry that resolves to a model file. With
   two or more models loaded, consecutive `GET /api/hardware` calls flip
   `loaded_model` between them at random. Clients (opencode sidebar meter,
   skein capacity view) render jittering memory numbers for an unchanged
   system state.

2. **Config reload triggers are dropped, not coalesced.** The reload closure
   (`llama-skein.go:191`) guards against overlapping reloads with a
   `reloading` flag, but a `triggerReload()` that arrives while a reload is in
   flight returns without effect. If a `PATCH /api/config/models/{id}` writes
   the config file after the in-flight reload's `LoadConfig` already read it,
   that patch is never applied and nothing retries. This bites the automated
   413-recovery path: opencode patches `ctx_size` and expects the next reload
   to pick it up.

3. **GGUF re-parsed on every hardware poll.** The CPU-host KV fallback added
   in e0550e5 calls `fitForModel()` from `handleAPIHardware`, which
   `gguf.ParseFile`s the model file on every request. Each connected TUI and
   skein poller hits this every 30s. Harmless today, wasteful, and it grows
   linearly with pollers.

## What

- Deterministic `loaded_model` selection: largest `model_mb` wins, ID as
  tie-break.
- Reload coalescing: a trigger that arrives mid-reload marks the state dirty;
  the finishing reload runs exactly one more pass. Extracted into a testable
  helper.
- A per-server GGUF parse cache keyed by (path, mtime) used by the fit paths.

## Constraints

- `contracts/llama-skein.openapi.json` owns the wire contract. None of these
  fixes changes a response shape; the `loaded_model` description gains a
  sentence about selection determinism (description-only change, regen
  required).
- The reload fix must not change the public behavior that `POST
  /api/config/reload` returns 202 immediately.
- Cache must not outlive a config generation: hot reload builds a fresh
  `server.Server`, which naturally drops the cache — rely on that, no TTL.

## Non-goals

- Reporting *all* running models in `loaded_model` (schema change; separate
  proposal if wanted).
- Cross-process reload locking (single daemon per host by design).
- Fit-engine math changes.
