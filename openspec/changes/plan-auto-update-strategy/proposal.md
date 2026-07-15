# Proposal: Auto-update strategy for llama-skein and its engines

**Status: decisions 2-4 confirmed by the user (event-driven checking, canary-first
with a real generation test, pin-and-graduate — never blind "always latest").
Decision 1 (scope) defaults to the recommendation below (engines only) unless
redirected. Decision 5 (visibility) resolved via a new history endpoint, see below.
Implementation not started — this change covers planning only.**

## Why

Two update surfaces exist today with very different levels of trust:

1. **The engines** (llama.cpp / lemonade-sdk/llamacpp-rocm per-host, via
   `POST /api/system/upgrade`) — now genuinely solid: ROCm-aware asset
   resolution that refuses rather than silently degrading, atomic
   write-temp-then-rename binary swap, graceful model unload before swap,
   smoke-test-then-rollback, and the fit-load-guard + wedge watchdog that make
   an oversized or unstable model far less likely to take the host down even
   if something does go wrong.
2. **llama-skein itself** (the Go proxy binary) — has *no* self-update
   mechanism at all today. Updates are manual (cross-compile, scp, restart)
   or were done by `com.skein.llama-fleet-update`, a launchd job installed
   2026-06-20 that auto-built `main` and restarted the whole fleet nightly at
   04:30, unattended — the job that (combined with the fit-engine bugs fixed
   this session) caused the OOM crash that started this whole investigation.
   That job is now disabled.

The user wants a smoother, less manual experience going forward but — for
good reason, given the 4am incident — explicitly does not want another
silent, unattended fleet-wide job. This proposal is about designing an
auto-update strategy that's automatic *and* safe *and* visible, not about
re-enabling what was disabled.

## Open decisions (need your input before any implementation)

### 1. Scope: engines only, or llama-skein's own binary too?

- **Engines only** (llama.cpp/lemonade-sdk): lower risk. A bad engine build
  fails its smoke test and rolls back automatically; the proxy itself never
  changes, so even a total engine failure leaves llama-skein's control plane
  (and the ability to manually intervene) intact.
- **Both**: a llama-skein self-update needs materially more care — if the
  *proxy* breaks, the whole host goes dark, including the ability to roll
  itself back. Would need its own bounded canary + health-check + rollback
  design, likely reusing the smoke-test/backup pattern but validated against
  actual request-serving, not just `--version`.
- **Recommendation**: start with engines only. llama-skein's own release
  cadence is far slower-moving than "did a new lemonade-sdk nightly drop";
  self-updating it can be a later, separate proposal once the engine
  auto-update has a track record.

### 2. Cadence: scheduled, event-driven, or manual-trigger-only?

- **Scheduled** (e.g. weekly): simplest, but reintroduces "a job runs
  unattended on a timer" — the exact shape of what caused the 4am incident,
  just less frequent. Needs strong safety gates to be acceptable.
- **Event-driven**: check for a new lemonade-sdk/llama.cpp release, and only
  act when one actually exists (vs. blindly re-running against the same
  version repeatedly). More complex (needs to track "last seen version" per
  host) but avoids pointless no-op runs.
- **Manual-trigger-only**: no automation at all — you run `skein providers
  upgrade <host>` when you choose. Zero surprise risk, but "smooth ride"
  probably means less of this, not more.
- **Recommendation**: event-driven, checking on a modest cadence (e.g. daily)
  but only *acting* when a genuinely new release is detected, and always
  through the canary gate in decision 3 — not scheduled-and-blind.

### 3. Safety gate: canary-first, or all-hosts-at-once?

Given z4, rocky, and proxmox run different GPU architectures (gfx1100,
gfx1100, gfx1201) and use MTP differently, a single bad release could affect
one host without affecting the others (as this session's investigation
showed, plainly).

- **Canary-first** (recommended): update one host, run a real generation
  test (not just a smoke test) against a live model, wait a defined
  observation window, then proceed to the rest only if healthy.
- **All-at-once**: faster, but a bad release hits the whole fleet
  simultaneously — no thanks, given today.

### 4. Pinning vs. always-latest

- **Always latest**: simplest, but a future lemonade-sdk or llama.cpp release
  could reintroduce instability (there is no guarantee upstream never
  regresses MTP support, RDNA3 handling, etc.) — auto-updating to `latest`
  blindly re-opens exactly the class of risk this session spent so much
  effort closing.
- **Pin + graduate**: track a specific known-good ref per host; new releases
  get flagged/canary-tested but don't auto-promote to "the pinned version"
  until the canary + observation window (decision 3) passes.
- **Recommendation**: pin + graduate. Slower, but "smooth ride" means never
  silently regressing.

### 5. Visibility — how do you want to be kept in the loop?

Given the 4am job's core failure was that you didn't know it existed, at
minimum any auto-update mechanism needs a place you can see what happened —
not just NDJSON progress events during the call, which vanish once the
request completes. Options: a persistent log file per host, a status
endpoint (`GET /api/system/upgrade/history`), a Slack/notification hook,
or just relying on `docs-skein` + memory notes being kept current (as this
session has been doing manually). Your call on how much infrastructure this
is worth.

## What

### Architectural split: llama-skein owns per-host primitives, skein owns fleet orchestration

llama-skein has no fleet-wide awareness today — it's a per-host proxy that
doesn't know about its siblings. Canary-then-promote inherently needs
cross-host coordination (host B must wait for host A's result). That
coordination belongs in **skein** (the supervisor — per the ecosystem docs,
it already "reads this proxy's health + model lists" across hosts), not in
llama-skein itself. llama-skein's job is to expose clean, per-host primitives
that skein's scheduler drives:

**New in llama-skein:**
- `GET /api/system/upgrade/check` — a dry check, reusing
  `resolvePrebuiltSource`'s existing resolution logic (so "what would I
  install" always matches what `/api/system/upgrade` would actually do) minus
  the download/install steps. Returns `{available, currentRef, latestRef,
  source}` without installing anything. This is the piece the daily
  event-driven check calls per host.
- `GET /api/system/upgrade/history` — resolves decision 5 (visibility): a
  small ring-buffer of past upgrade attempts (timestamp, from-ref, to-ref,
  result, canary-or-promoted) persisted per host, so "what happened and when"
  survives past the NDJSON progress stream of a single call. This is the
  place to look instead of "did anyone remember to check the logs."
- The existing `POST /api/system/upgrade` (prebuilt method) stays the
  install primitive — already has smoke-test + rollback; no change needed
  for this proposal beyond recording into the new history buffer.

**New in skein:**
- A scheduled task (daily, per decision 2) that calls `check` on every known
  llama-skein host.
- On any host reporting `available: true`: pick the designated canary host
  (config, not hardcoded — z4, rocky, and proxmox all differ in
  architecture, so which one is "canary" should be an explicit choice, not
  assumed), run `POST /api/system/upgrade` against it, then run a REAL
  generation request against one of its currently-configured models (not
  just trust the built-in smoke test) — reusing whatever provider-health
  check skein already does for its normal monitoring.
- On canary success + an observation window (needs a decision: how long —
  minutes, hours?): promote by running the upgrade against the remaining
  hosts, each still going through the smoke-test + rollback llama-skein
  already provides.
- On canary failure: do NOT touch the other hosts; surface the failure
  (skein's existing notification/status surface, or the new history
  endpoint above) and leave everything pinned at the last-known-good ref.
- Per-host `pinnedRef` state lives wherever skein already keeps
  per-provider config (not duplicated in llama-skein, which stays
  fleet-unaware).

## Non-goals (for this pass)

- llama-skein self-updates (see decision 1 — deferred, not in scope).
- Windows/other platforms not currently in the fleet.
- Automatically discovering *new* hosts to manage — scope stays to
  z4/rocky/proxmox (and m3/m5, which don't use this engine-upgrade path at
  all today — MLX has no equivalent mechanism).
- Deciding the exact observation-window duration and which host is the fleet
  canary — these are configuration choices to make at implementation time,
  not architectural ones.
