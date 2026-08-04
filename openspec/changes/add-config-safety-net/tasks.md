# Tasks: add-config-safety-net

## STATUS (2026-08-02): implemented, tests green, not yet deployed to z4

## 1. Spec first
- [x] 1.1 Extend `contracts/llama-skein.openapi.json`: `POST /api/config/validate`,
      `GET /api/config/history`, `POST /api/config/rollback`; reload response
      `errors[]`/`warnings[]`; `/health` `config_status`; model (`ModelHealth`)
      `warnings[]`. (`/v1/models` warnings deferred — see 4.4.)
- [x] 1.2 `go generate ./pkg/apicontract && gofmt -w pkg/apicontract/llama_skein.gen.go`;
      `make check-codegen` verified clean (codegen re-run is idempotent).

## 2. Snapshots + retention
- [x] 2.1 Snapshot writer: copy-on-accept into `config-history/` with JSON sidecar
      (actor, summary). `internal/config/history.go` + `runtime_state.go`.
      Summary is handler-supplied (e.g. "patched model X"), not an auto-diff —
      simpler and equally informative; auto-diffing was in the original draft
      but handlers already know what they're doing.
- [x] 2.2 Pruning: keep max(`keep` newest, within `maxAgeDays`), hard cap 200;
      runs on every snapshot. `configHistory: {keep, maxAgeDays}` (both
      `*int`, nil = default 20/30); `keep: 0` disables new snapshots without
      deleting existing ones.
- [x] 2.3 One-time legacy migration: `MigrateLegacyBackups` sweeps
      `config.yaml.bak*` into history (actor `legacy-bak`), removes originals.
      Runs once at startup in `llama-skein.go`.
- [x] 2.4 `GET /api/config/history` + `POST /api/config/rollback` handlers.
      Rollback does NOT self-snapshot (see `RollbackConfig`'s doc comment):
      the reload it triggers is what snapshots the outgoing config, via the
      same mechanism every other write uses — no special-casing, and no
      double-snapshot.

## 3. Loud validation
- [x] 3.1 Reload path: parse+validate before swap; HTTP 422 with error detail on
      failure; old config keeps serving. Regression test
      `TestServer_ConfigReload_InvalidYAMLIsRejectedLoudly` asserts status !=
      "reloading" on invalid YAML (the 2026-07-30 silent no-op).
- [x] 3.2 `POST /api/config/validate` dry-run (body or on-disk file). Never
      writes, never reloads — verified by
      `TestServer_ConfigValidate_DryRunNeverWritesTheFile`.
- [x] 3.3 `/health` `config_status` incl. `stale_since` (set once, held across
      repeated failures — doesn't reset on every retry).

## 4. Warnings (annotate, never reject)
- [x] 4.1 Warning framework: `internal/config.Warning{Model,Flag,Message,Source}`,
      `Server.configWarnings(cfg)` aggregates sources.
- [x] 4.2 flash-attn × GPU-family table (gfx1100/1101/1102), consulting
      `cfg.Tuning.GfxTarget` override with the same precedence
      `internal/tuning` already uses.
- [ ] 4.3 Arch-failure memory: on load failure, record
      `(gguf arch, engine binary hash) → error`; replay as warning on later
      configs. **Deferred** — scoped as its own follow-up, not attempted here.
- [~] 4.4 Surface warnings in reload/validate responses and `/health`
      (`ModelHealth.warnings` schema field added but not yet populated per-
      model in the health handler — only the reload/validate paths populate
      warnings today). `/v1/models` warnings **deferred** (lower value; a
      wedge risk belongs in `/health`/reload, not a model listing).
- [x] 4.5 No code path converts a warning into a rejection — verified by
      inspection (validate/reload only ever fail on parse/structural errors,
      never on `configWarnings` output) and by
      `TestFlashAttnWarnings_*`/`TestServer_ConfigReload_ValidConfigAccepted`.

## 5. Ship
- [x] 5.1 `go build ./... && go vet ./... && go test ./...` — 1140 passed, 30
      packages. `make test-dev` ran clean (staticcheck itself couldn't run —
      pre-existing Go-toolchain-version mismatch in this environment,
      unrelated to this change; `|| true` in the Makefile target already
      tolerates it). `make test-all` not yet run (long-running concurrency
      suite) — do before merge.
- [ ] 5.2 Regenerate opencode TS client; surface model warnings in the ctx/model
      UI (separate opencode-skein PR) — not started.
- [ ] 5.3 Update `docs/openapi-contract.md` + `config-schema.json` — not started.
- [ ] 5.4 Deploy to z4; verify: break the config on purpose → 422 + health flag;
      roll back via API; legacy .bak files migrated. **Deliberately not done
      in this session** — z4 was mid-use by the operator; deploying means
      rebuilding and restarting the live llama-skein binary. Do this as an
      explicit, confirmed step, not a side effect of merging.
