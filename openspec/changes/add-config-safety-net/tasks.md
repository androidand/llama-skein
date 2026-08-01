# Tasks: add-config-safety-net

## 1. Spec first
- [ ] 1.1 Extend `contracts/llama-skein.openapi.json`: `POST /api/config/validate`,
      `GET /api/config/history`, `POST /api/config/rollback`; reload response
      `errors[]`/`warnings[]`; `/health` `config_status`; model `warnings[]`.
- [ ] 1.2 `go generate ./pkg/apicontract && gofmt -w pkg/apicontract/llama_skein.gen.go`;
      `make check-codegen` green.

## 2. Snapshots + retention
- [ ] 2.1 Snapshot writer: copy-on-accept into `config-history/` with JSON sidecar
      (actor, change summary diffed at the model-entry level).
- [ ] 2.2 Pruning: keep max(20 newest, ≤30 days), hard cap 200; run on every write.
      Config knob `configHistory: {keep, maxAgeDays}`; `keep: 0` disables.
- [ ] 2.3 One-time legacy migration: sweep `config.yaml.bak*` into history
      (actor `legacy-bak`), leave the config dir clean.
- [ ] 2.4 `GET /api/config/history` + `POST /api/config/rollback` handlers
      (rollback snapshots current state first — a rollback is itself a change).

## 3. Loud validation
- [ ] 3.1 Reload path: parse+validate before swap; HTTP 422 with error detail on
      failure; old config keeps serving. Regression test: invalid YAML reload
      must NOT return `{"status":"reloading"}` (the 2026-07-30 silent no-op).
- [ ] 3.2 `POST /api/config/validate` dry-run (body or on-disk file).
- [ ] 3.3 `/health` `config_status` incl. `stale_since` when the on-disk file is
      broken but old config serves.

## 4. Warnings (annotate, never reject)
- [ ] 4.1 Warning framework on validation: `[]{model, flag, message, source}`.
- [ ] 4.2 flash-attn × GPU-family table (gfx1100 first), sourced from the
      existing tuning/gfx detection.
- [ ] 4.3 Arch-failure memory: on load failure, record
      `(gguf arch, engine binary hash) → error`; replay as warning on later
      configs. Persist beside history.
- [ ] 4.4 Surface warnings in reload/validate responses, `/health`, `/v1/models`.
- [ ] 4.5 Assert: no code path converts a warning into a rejection.

## 5. Ship
- [ ] 5.1 `go build ./... && make test-dev`, then `make test-all`.
- [ ] 5.2 Regenerate opencode TS client; surface model warnings in the ctx/model
      UI (separate opencode-skein PR).
- [ ] 5.3 Update `docs/openapi-contract.md` + `config-schema.json`.
- [ ] 5.4 Deploy to z4; verify: break the config on purpose → 422 + health flag;
      roll back via API; legacy .bak files migrated.
