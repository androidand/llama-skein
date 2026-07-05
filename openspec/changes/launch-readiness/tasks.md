# Tasks: Launch readiness

Ordering: Phase 1 = safe/mechanical (agent, unsupervised). Phase 2 = needs a
running app, CI, or human judgment. Phase 3 = owner-only blockers.

Ground rules for every task: work only in `~/dev/llama-skein`; never touch
`contracts/llama-skein.openapi.json` or `pkg/apicontract/`; never stage or
revert the pre-existing dirty files (`git status --porcelain` before starting —
anything already listed is untouchable); commit each task by explicit path on
the current branch; never push without the owner.

## Phase 1 — Safe / mechanical

- [x] 1. Rewrite `README.md` for fork identity. Keep the first-line fork banner
  but restructure: (a) title `# llama-skein`; (b) one-paragraph pitch (inference
  proxy for the skein ecosystem, forked from mostlygeek/llama-swap); (c) a
  "Divergence from upstream" section listing the fork extensions — copy the
  bullet list from `ECOSYSTEM.md` ("Fork extensions over upstream") and
  reconcile with the `features` array in `skein.json`; (d) replace the three
  badges on lines 8-10 (they point at mostlygeek/llama-swap) with
  androidand/llama-skein equivalents or delete them; (e) keep upstream's
  feature/configuration docs below, clearly attributed ("inherited from
  llama-swap"); (f) drop the upstream Star History section (line ~283).
  - Validation: `grep -c 'mostlygeek' README.md` — remaining hits are only
    attribution links and upstream-issue references, not badges/install paths.
- [x] 2. Fix `README.md` installation section: label Docker
  (`ghcr.io/mostlygeek/*`), Homebrew tap, and WinGet as **upstream llama-swap**
  channels that do not contain fork features; make "Building from source" the
  primary fork install, cloning `https://github.com/androidand/llama-skein`
  (currently line ~184 clones mostlygeek/llama-swap) with `make clean all`,
  binary in `build/`. Mention Go + Node prerequisites.
  - Validation: README build-from-source URL is androidand/llama-skein.
- [x] 3. Retarget `.github/workflows/sync-upstream.yml` from the deleted branch
  `feat/model-state-and-lifecycle-api` to `main` (two places: `ref:` in checkout
  and the `git push --force-with-lease origin ...` line). Keep the Monday cron
  and dry_run input.
  - Validation: `grep -c 'feat/model-state-and-lifecycle-api' .github/workflows/sync-upstream.yml` = 0.
- [x] 4. Guard `.github/workflows/unified-docker.yml`: it pushes to
  `ghcr.io/mostlygeek/llama-swap` (lines ~114, ~126) which this fork does not
  own. Add the same repository guard `containers.yml` line 89 uses
  (`if: github.repository == 'mostlygeek/llama-swap'`) at job level, so the
  workflow is inert on the fork until the owner retargets it (Phase 3 task 15).
  - Validation: job-level `if:` present; `actionlint` or YAML parse passes.
- [x] 5. Fix stale branch/name references in docs:
  `ECOSYSTEM.md` "Our branch: `feat/model-state-and-lifecycle-api`" → `main`
  (and the two `git push ... feat/model-state-and-lifecycle-api` examples);
  `AGENTS.md` line 8 "located in ui/" → "located in ui-svelte/".
  - Validation: `git grep -l 'feat/model-state-and-lifecycle-api'` returns
    nothing outside git history/openspec archives.
- [x] 6. Add `docs-skein/` to `.gitignore` (the private companion repo must
  never be committable from inside this working tree; an empty `docs-skein/`
  dir already exists here).
  - Validation: `git check-ignore docs-skein` exits 0.
- [x] 7. Replace `/Users/andreas/...` absolute paths with portable sibling
  notation (`~/dev/skein`, `~/dev/opencode/packages/opencode`, or "the sibling
  checkout") in: `AGENTS.md` (line 35), `ECOSYSTEM.md` (line 3),
  `docs/openapi-contract.md` (lines 27, 68, 85, 100, 116, 122, 130, 141, 146,
  151 — note several also say `llama-swap` where the dir is now `llama-skein`;
  fix those too). Do NOT touch `scripts/` in this task (Phase 2 task 10).
  - Validation: `git grep -l '/Users/andreas' -- '*.md' ':!openspec/' ':!scripts/'`
    returns nothing.
- [x] 8. Remove committed pipeline residue from openspec: delete the three
  empty machine-generated changes `openspec/changes/intake-20260601-230316-bde2bc/`,
  `openspec/changes/intake-20260601-231152-298121/`,
  `openspec/changes/intake-20260601-234615-314225/` (all "No tasks" per
  `openspec list`), and delete
  `openspec/changes/add-persistent-user-profile-saving/.skein/coder-context.md`
  (contains local worktree paths). `git rm` by explicit path only.
  - Validation: `openspec list` no longer shows intake-* entries.

### Phase 1 completion notes (2026-07-05)

All Phase 1 tasks (1-8) executed on `main`, working tree otherwise clean
except the owner's pre-existing dirty files (proxy/metrics_monitor.go,
proxy/proxymanager.go, proxy/proxymanager_skein.go, and untracked
proxy/proxymanager_metrics.go, proxy/proxymanager_reserve.go,
proxy/proxymanager_warmup.go, openspec/changes/decouple-roles-from-go-code/),
none of which were staged, modified, or discarded. `LICENSE.md` copyright
(task 16) is Phase 3/owner-only and was correctly left untouched.

Commits:
- `3f89549` docs(readme): rewrite README for fork identity (tasks 1-2)
- `2e84488` ci: fix sync-upstream branch and guard unified-docker registry push (tasks 3-4)
- `0df3bb9` docs: scrub stale branch refs and personal absolute paths (tasks 5-7)
- `1f6920e` chore: remove empty intake changes and local worktree residue (task 8)

Verification: `go build ./...` exit 0; both workflow YAML files parse with
`python3 -c "import yaml; yaml.safe_load(open(...))"` (actionlint not
installed locally); all per-task grep/git-grep/check-ignore/openspec-list
validations in tasks 1-8 pass as written. Nothing pushed.

## Phase 2 — Needs running app, CI, or human judgment

- [ ] 9. Verify the fork quickstart on a clean clone: `git clone` the repo to a
  temp dir, `make clean all`, confirm the binary lands in `build/` and
  `./build/llama-skein-* --help` (or `--version`) works, and the minimal
  config from README serves `/v1/models`. Fix README where reality disagrees.
  - Validation: documented quickstart executes end-to-end on a clean machine.
- [ ] 10. Decide + execute placement of the fleet deploy machinery:
  `scripts/fleet-deploy.sh` and `scripts/launchd/com.skein.llama-fleet-update.plist`
  encode the owner's personal topology (ssh aliases `proxmox`/`rocky`, LXC 1016,
  `/Users/andreas/bin/...`). Options: (a) move both to the private
  `~/dev/docs-skein` repo and leave a stub note, or (b) genericize (host list
  from an untracked `fleet.conf` / env vars) and keep as a reusable example.
  Requires the owner's fleet to test whichever path is chosen.
  - Validation: no personal hostnames/paths in tracked `scripts/`; the fleet
    launchd job still deploys successfully afterwards.
- [ ] 11. Get fork CI green and visible: confirm GitHub Actions is enabled on
  androidand/llama-skein, `go-ci.yml` + `ui-tests.yml` pass on `main`, and the
  README CI badge (task 1) references this repo's workflow run, not upstream's.
  - Validation: badge renders green from androidand/llama-skein actions.
- [ ] 12. Run `make test-all` and `make check-codegen` on `main` and record
  results; fix or file issues for any failure — a public repo whose own test
  target fails on a fresh clone is not launch-ready.
  - Validation: `make test-all` exit 0 (or failures triaged into issues).
- [ ] 13. Review whether `.skein/`, `.opencode/`, and `ai-plans/` stay tracked
  in a public repo. They are personal agent-pipeline config/planning notes, not
  product. Note: `.skein/agents/*.md` deletions are already pending in the
  owner's dirty working tree (change `decouple-roles-from-go-code`) — do not
  preempt; coordinate after that change lands. Judgment call: keeping them is
  harmless but noisy; removing loses the "how this repo is developed" story.
  - Validation: decision recorded in this tasks file; repo reflects it.
- [ ] 14. Upstreaming pass: from `skein.json` `features[].upstreamable`, pick
  the top candidates tagged `yes`/`partial` that are skein-agnostic (starting
  set: slot-cancel on client disconnect, HTTP 413 pre-flight context guard,
  GGUF metadata array cap raise 6129b49). For each: check the code extracts
  cleanly against `upstream/main` (`git fetch upstream` first), and prepare a
  standalone branch + upstream PR. Human judgment on upstream appetite;
  interaction with upstream maintainer required.
  - Validation: at least one upstream PR opened or an explicit "not now"
    recorded per candidate.

## Phase 3 — Owner-only blockers

- [ ] 15. Confirm the launch angle (public fork now, upstream PRs
  opportunistically — as argued in proposal.md) or redirect. This gates the
  README tone (task 1) and whether unified-docker.yml gets retargeted to a
  fork-owned `ghcr.io/androidand/llama-skein` registry instead of just guarded
  (task 4).
- [ ] 16. LICENSE.md: append a fork copyright line under the existing MIT text
  (e.g. "Portions copyright (c) 2025-2026 <owner name> (llama-skein fork)").
  Owner must confirm the public name/identity to use — commits use
  "AndreasS <tantonet@gmail.com>".
- [ ] 17. GitHub repo settings (web UI, owner credentials): repo description +
  topics (llama-cpp, mlx, vllm, model-swapping, openai-api), decide whether
  Releases are published under androidand (goreleaser `release.yml` is already
  de-tapped for the fork), enable/verify Actions and Dependabot, and check
  there are no leftover upstream release artifacts attached to the fork.
- [ ] 18. Final pre-flip secret sweep: this audit's spot checks
  (`git log --all -S` for hf_/ghp_/github_pat_/sk-ant/AKIA/private-key headers)
  found nothing, but before announcing, run a full-history scanner
  (`gitleaks detect --source .` or trufflehog) and review its report. No
  rewrite expected; owner signs off.
- [ ] 19. Settle the ~25 pre-existing dirty working-tree files (in-flight
  fit/MTP/perf work + `.skein/agents` deletions): commit, finish, or shelve —
  an announced repo should not have a months-old dirty main. Owner only; no
  agent may touch these files.
