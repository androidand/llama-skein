# Proposal: Config safety net — snapshot, surface, warn (never force)

## Why

An autonomous agent replaced `/etc/llama-skein/config.yaml` wholesale during a
model deploy (2026-08-01): the original entries were lost, the default model
pointed at a GGUF the engine could not load, and `--flash-attn on` was set on a
gfx1100 host where it is known to wedge the GPU. Recovery took hours and was
only possible because a *manual* mirror of the config existed in a companion
repo. Separately, the same week, an invalid YAML edit made every
`POST /api/config/reload` **silently no-op**: the endpoint answered
`{"status":"reloading"}` while keeping stale in-memory config, hiding the
breakage for hours.

Root cause is not the agent: any root-privileged actor (human or agent) can
write the file, and that will happen again. The defense is to make any mess
**loud immediately** and **reversible in one command** — while never blocking
an informed user from trying something unusual. The trained-context ceiling
episode set the principle: *the system informs, the user decides.*

## What changes

1. **Snapshot on every accepted config change.** Before applying a reload or a
   config-mutating API call, copy the previous config into
   `<config-dir>/config-history/config-<UTC-timestamp>.yaml` together with a
   one-line JSON sidecar (`actor`: `"file-reload"` or the API route; `summary`:
   models added/removed/changed). Retention is bounded (see below). New
   endpoints: `GET /api/config/history` (list snapshots + summaries) and
   `POST /api/config/rollback` (`{"ref": "<snapshot-id>"}` → snapshot current,
   restore ref, apply, report). The scattered ad-hoc `config.yaml.bak*` files
   llama-skein and operators create today are superseded.

2. **Validation failures are loud.** `POST /api/config/reload` parses and
   validates *before* answering: on failure it returns HTTP 422 with the parse
   error and keeps serving the old config — and `GET /health` gains
   `config_status: {valid: bool, error?: string, stale_since?: ts}` so a broken
   file is visible everywhere until fixed. New `POST /api/config/validate`
   dry-runs a candidate config (request body or the on-disk file) and returns
   errors + warnings without applying.

3. **Warnings, never enforcement.** Validation annotates but never rejects a
   loadable config. Warning sources (initial set):
   - `--flash-attn on` where the detected GPU family is known-problematic
     (gfx1100/RDNA3): *"known to wedge this GPU in most builds — usable, expect
     hangs"*.
   - a model whose GGUF `general.architecture` previously failed to load on the
     configured engine binary (llama-skein already observes load failures;
     remember `(arch, engine-binary-hash) → last_error` and replay it as a
     warning).
   - `--ctx-size` beyond the GGUF's trained context (already surfaced by fit;
     unify wording).
   Warnings appear in the reload/validate response, in `/health` per model, and
   in `/v1/models` metadata so UIs (opencode) can render them. **No warning is
   ever promoted to an error.** There is no `overrideHardwarePolicy` flag
   because there is no policy to override — trying odd things is a supported
   workflow on a research fleet.

## Snapshot retention (TTL)

Prune `config-history/` on every write, keeping whichever is larger:
- the **20 most recent** snapshots, and
- everything younger than **30 days**,
with an absolute cap of 200 snapshots as a runaway backstop (configurable via
`configHistory: {keep: N, maxAgeDays: D}`; `keep: 0` disables snapshots).
Snapshots are small YAML files — the cap exists for hygiene, not disk. On
startup, if legacy `config.yaml.bak*` files exist beside the config, migrate
them into `config-history/` once (mtime as timestamp, actor `"legacy-bak"`) so
the directory stays the single source of history and the clutter disappears.

## Non-goals (v1)

- No engine registry / provenance subsystem (revisit when a second incident
  proves the need; the arch-failure warning covers the sharpest edge).
- No git dependency, no external mirror requirement.
- No foreign-VRAM / maintenance-mode state (separate change; needed for
  training windows but independent of config safety).
- No blocking of any user-chosen flag, ever.

## API surface (spec-first)

`contracts/llama-skein.openapi.json` gains: `POST /api/config/validate`,
`GET /api/config/history`, `POST /api/config/rollback`; the reload response
schema gains `errors[]`/`warnings[]`; `/health` gains `config_status`; model
entries in `/health` and `/v1/models` gain `warnings[]`. Go types regenerated
via `go generate ./pkg/apicontract`; opencode TS client via
`bun run build:llama-skein-client`; skein callers updated last.
