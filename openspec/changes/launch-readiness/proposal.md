# Proposal: Launch readiness for llama-skein (public fork)

## Why

llama-skein is a fork of mostlygeek/llama-swap, currently ~50 commits ahead
(+50/-1 per `make upstream-check`, 2026-07-04). "Launch ready" for THIS repo
does not mean shipping a new product — the repo is already reachable at
`github.com/androidand/llama-skein` and the fleet deploy clones it anonymously.
It means a stranger landing on the repo must immediately understand **what this
fork is, why it diverges, and how to run it** — and must not trip over the
owner's personal infrastructure leaking through docs, scripts, and CI.

Audit findings (2026-07-04, read-only):

- **No secrets** in tracked files or in history spot-checks (`git log --all -S`
  for `hf_`, `ghp_`, `github_pat_`, `sk-ant`, `AKIA`, private key headers — all
  clean; `HF_TOKEN`/API keys are env-var reads only; `.skein/config.yaml` token
  fields are empty placeholders). No history rewrite needed.
- **Identity is upstream's, not the fork's**: README.md is upstream's README
  with a 3-line fork banner. Badges (downloads/CI/stars), Docker images
  (`ghcr.io/mostlygeek/...`), Homebrew tap, WinGet, release links, and
  build-from-source clone URL all point at mostlygeek/llama-swap. A visitor
  gets upstream's pitch and *upstream's* install instructions.
- **Stale branch references**: ECOSYSTEM.md and
  `.github/workflows/sync-upstream.yml` still name
  `feat/model-state-and-lifecycle-api`; the actual default branch is `main`.
  The scheduled sync workflow would rebase/push the wrong branch.
- **CI pushes to a registry the fork doesn't own**:
  `.github/workflows/unified-docker.yml` targets `ghcr.io/mostlygeek/llama-swap`
  with no repository guard (containers.yml has one).
- **Personal infra in tracked files** (private info, not secrets):
  `scripts/fleet-deploy.sh` + `scripts/launchd/com.skein.llama-fleet-update.plist`
  encode the owner's fleet topology (ssh aliases `proxmox`/`rocky`, LXC 1016,
  `/Users/andreas/...` paths); `/Users/andreas/dev/...` absolute paths appear in
  AGENTS.md, ECOSYSTEM.md, `docs/openapi-contract.md`, and
  `openspec/changes/add-persistent-user-profile-saving/.skein/coder-context.md`.
- **.gitignore gap**: `docs-skein/` (the *private* companion repo, documented to
  be cloned into `~/dev/docs-skein` but an empty `docs-skein/` dir exists inside
  this working tree) is not ignored — one wrong clone location + `git add`
  away from publishing private deploy docs.
- **Workflow noise tracked**: `.skein/`, `.opencode/`, `ai-plans/`, and three
  empty machine-generated `openspec/changes/intake-*` changes are public but
  are personal agent-pipeline config, not product.
- **Hygiene is otherwise good**: LICENSE.md (MIT, upstream copyright) present;
  no debug prints, no TODO/FIXME in `internal/`/`proxy/`/`pkg/`, no
  `console.log` in `ui-svelte/src`; `logs/`, `bin/`, `Makefile.bak` correctly
  ignored; `contracts/llama-skein.openapi.json` intact (source of truth, not
  touched by this change).

### Launch angle: public fork vs upstreaming

Both paths stay open; they are not mutually exclusive:

- **Public fork (primary, near-term)** — the fork is load-bearing for the skein
  ecosystem (skein imports `pkg/apicontract`; opencode generates a TS client
  from `contracts/`). It must exist publicly regardless. Cost: a README that
  honestly states divergence and fork-specific install; keep `make
  upstream-check` + rebase discipline (already in CLAUDE.md).
- **Upstreaming (secondary, ongoing)** — `skein.json` already tags each feature
  with `upstreamable: yes|partial|no`. Best candidates are self-contained,
  skein-agnostic fixes (slot-cancel on disconnect, 413 pre-flight context
  guard, GGUF metadata cap raise). Upstreaming shrinks the rebase surface but
  can never replace the fork: `/api/skein/*`, the OpenAPI contract, mDNS
  registration, and the fit engine are ecosystem-specific by design.

So: launch as a **clearly-labelled public fork**, and file upstream PRs for the
tagged-upstreamable features opportunistically (Phase 2 tasks).

## What Changes

1. **README rewrite (fork identity first)** — lead with what llama-skein is,
   the divergence list (from ECOSYSTEM.md / skein.json), fork-correct install
   (build from source; upstream channels labelled as upstream's), correct
   badges, retained upstream attribution.
2. **CI/workflow correctness** — sync-upstream.yml retargeted to `main`;
   unified-docker.yml guarded (or retargeted to a fork-owned registry);
   README CI badge points at this repo's workflow.
3. **Private-info scrub** — `docs-skein/` gitignored; `/Users/andreas` paths
   replaced with `~/dev/...` sibling-checkout notation in docs; fleet deploy
   scripts either genericized (env-driven host list) or moved to the private
   docs-skein repo (owner decision); stale intake-* openspec changes and the
   committed `.skein/coder-context.md` removed.
4. **Licensing/attribution** — fork copyright line appended to LICENSE.md
   (owner confirms public identity).
5. **Owner decisions recorded** — launch angle confirmation, GitHub repo
   metadata (description/topics/releases), whether `.skein/`, `.opencode/`,
   `ai-plans/` stay tracked, and which features get upstream PRs first.

## Impact

- Affected: `README.md`, `ECOSYSTEM.md`, `AGENTS.md`, `.gitignore`,
  `LICENSE.md`, `.github/workflows/{sync-upstream,unified-docker}.yml`,
  `docs/openapi-contract.md`, `scripts/fleet-deploy.sh`,
  `scripts/launchd/*.plist`, `openspec/changes/intake-*`,
  `openspec/changes/add-persistent-user-profile-saving/.skein/`.
- NOT touched: `contracts/llama-skein.openapi.json` (cross-repo source of
  truth), `pkg/apicontract/` (generated), any Go/TS runtime code, git history
  (no rewrite — none needed), the ~25 pre-existing dirty working-tree files
  (owner's in-flight work, including the pending `.skein/agents/*` deletions).
- Specs: one delta, `specs/public-launch-readiness/spec.md`. Note: this repo's
  existing changes use freeform spec.md files that do **not** pass
  `openspec validate` (verified 2026-07-04: `verified-model-readiness` and
  `add-model-fit-engine` both fail with "No delta sections found"). This
  change's delta uses the strict `## ADDED Requirements` + `#### Scenario:`
  format so `openspec validate launch-readiness` passes.
- Risk: low. Everything here is docs/CI/metadata; the only behavioral surface
  is CI workflows, which currently misbehave (wrong branch, unowned registry).
