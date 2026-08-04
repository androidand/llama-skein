# Tasks: Add persistent user profile saving to `/api/skein/config`

## Correction (2026-08-03)

The Ralph-loop-generated task list this file originally held was hollow —
every phase marked `[x]` done, but no `UserProfile`/`ProfileStore`/handler
code, and none of the referenced paths (`internal/server/models`,
`internal/server/repo`, `internal/server/routes`, `internal/server/service`
— none of which exist in this codebase's actual flat `internal/server`
layout) were ever committed anywhere in the repo's history. This is the same
`NO_PROGRESS_REPEATED` incident (2026-06-02) documented on
`backup/27b8f95-original`'s `.skein/blocked-reason.md`: the coder loop got
stuck three times and never landed real code, yet the checkboxes advanced
and the dangling references made it into `server.go` (disabled in `60c3cba`
to unblock the build).

Replaced with an honest task list reflecting the actual implementation.

## What was built

- [x] `apicontract.UserProfile`/`UserProfileState`/`PowerProfile` — these
  generated types already existed (the OpenAPI contract was real; only the
  Go implementation behind it was missing).
- [x] `ProfileStore` (`internal/server/apiprofile.go`): JSON file at
  `~/.llama-skein/skein/profile.json`, atomic write (temp file + rename).
- [x] `validateUserProfile`/`validateSchedule`: enforces the contract's
  documented ranges (power_limit_pct 1-99, temp_target_celsius 40-100) and
  the `HH:MM-HH:MM` schedule format `thermal.Manager`'s own scheduler parses.
- [x] `GET /api/skein/config`, `POST /api/skein/config`,
  `GET /api/skein/config/default` — wired onto the already-shipped
  `thermal.Manager` (`Apply`/`Restore`/`StartSchedule`), not a parallel
  implementation of GPU power control.
- [x] `silent_mode=true` on a host with no GPU power control returns 503 per
  the contract, rather than silently accepting a no-op preference.
- [x] Saved profile re-applies on server startup — the actual point of
  "persistent": silent mode now survives a restart without manual DPM/APU
  re-tuning. Takes over from the YAML `silent_mode.schedule` config block
  once a profile has ever been saved via the API.
- [x] 21 tests (`internal/server/apiprofile_test.go`): store roundtrip,
  validation edge cases, all three handlers, the unavailable-host 503 path,
  the on→off transition, startup re-apply plumbing.
- [x] `go build ./... && go test ./...` green (1156 tests, 30 packages, at
  the point this change landed).

## Not done / follow-up

- [ ] Live verification on a real AMD GPU host with working `rocm-smi`/sysfs
  power control — this session verified the unavailable-host path (503,
  intent-tracking) since no such host was reachable; the actual power-limit
  write path (`thermal.Apply`) is pre-existing, shipped code, not new here.
- [ ] `/api/system/version` does not surface the active profile — not
  requested by the original proposal, noted as a possible follow-up only.
