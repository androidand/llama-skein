# Spec delta: auto-update (plan-auto-update-strategy)

**Proposed, not yet implemented** — pending the configuration decisions in
tasks.md item 5. Internal ops endpoints, not part of the OpenAPI-generated
wire contract (matching `/api/system/upgrade`'s existing precedent).

## ADDED (proposed)

### Dry-run release check

- `GET /api/system/upgrade/check` MUST report whether a newer release exists
  for this host's already-resolved engine source, without installing
  anything. It MUST reuse the same source-resolution logic
  (`resolvePrebuiltSource`) that a real `POST /api/system/upgrade` would use,
  so a caller can trust that "available" reflects what an actual upgrade
  would do.
- Response MUST include: whether a newer release is available, the
  currently-installed ref (best-effort, from the running binary's reported
  version), the latest available ref, and which source it resolved to
  (tailored / untailored / refused, matching the existing upgrade
  `source.note` semantics).

### Upgrade history

- `GET /api/system/upgrade/history` MUST return a bounded, persistent record
  of past upgrade attempts on this host: timestamp, previous ref, target ref,
  outcome (success / rolled back / refused), and the failure reason when
  applicable. This MUST survive past the lifetime of any single upgrade
  request's NDJSON progress stream.

## Non-goals (this delta)

- Fleet-wide orchestration (canary selection, promotion, observation
  windows) — that lives in skein, not llama-skein, per the architectural
  decision in proposal.md.
- Automatic execution of an upgrade from the check endpoint — `check` is
  read-only; only `POST /api/system/upgrade` installs anything.
