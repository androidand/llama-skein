# config-management (delta)

## ADDED Requirements

### Requirement: Config changes are snapshotted before apply
Every accepted config mutation (file reload or config-mutating API call) SHALL
first copy the previously-active config into `config-history/` with a
timestamped filename and a JSON sidecar recording the actor and a model-level
change summary.

#### Scenario: agent replaces the config wholesale
- GIVEN a valid config is serving
- WHEN any actor overwrites the config file and triggers a reload
- THEN the previous config exists in `config-history/` and
  `POST /api/config/rollback` restores it in one call

### Requirement: Snapshot retention is bounded
`config-history/` SHALL be pruned on every write, retaining whichever is
larger: the `keep` most recent snapshots (default 20) or all snapshots younger
than `maxAgeDays` (default 30), never exceeding 200 total. Legacy
`config.yaml.bak*` files SHALL be migrated into history once at startup.

#### Scenario: years of operation do not accumulate clutter
- GIVEN daily config changes for a year
- WHEN the operator lists the config directory
- THEN it contains the active config and a bounded `config-history/`, no loose
  `.bak` files

### Requirement: Invalid configs fail loudly, old config keeps serving
`POST /api/config/reload` SHALL validate before applying. On failure it SHALL
return HTTP 422 with the underlying error and continue serving the previous
config, and `GET /health` SHALL expose `config_status` with the error and
`stale_since` until a valid config is applied.

#### Scenario: the 2026-07-30 silent no-op
- GIVEN a config file with a YAML syntax error
- WHEN `POST /api/config/reload` is called
- THEN the response is 422 with the parse error (NOT `{"status":"reloading"}`)
  and `/health` reports `config_status.valid: false`

### Requirement: Validation warns but never blocks
Validation SHALL annotate risky-but-loadable configuration with warnings
(known-problematic flag for the detected GPU family, GGUF architecture that
previously failed on the configured engine, ctx beyond trained context) in the
reload/validate responses, `/health`, and `/v1/models`. No warning SHALL ever
cause a config or model to be rejected.

#### Scenario: user tries flash-attn on RDNA3 anyway
- GIVEN a gfx1100 host and a model cmd containing `--flash-attn on`
- WHEN the config is applied
- THEN the model is registered and loadable, and its entry carries a warning
  describing the known wedge risk
