# llama-skein — agent instructions

**llama-skein** is a fork of [mostlygeek/llama-swap](https://github.com/mostlygeek/llama-swap)
extended for the Skein ecosystem. The folder is named `llama-swap/` locally; the Go module
and GitHub remote are `github.com/androidand/llama-skein`.

It is a lightweight transparent proxy that provides automatic model swapping for
llama.cpp's server, and the inference layer of the skein ecosystem:

```
skein (supervisor) → opencode-skein (agent runner) → llama-skein (inference proxy)
```

@ECOSYSTEM.md

## Workflow: use the skein-dev skill

**The `skein-dev` skill is the source of truth for how to work in this ecosystem.**
It covers the design-first OpenAPI flow, code generation, verification, and fleet
deploy across all four repos. Invoke it before an API change, a regeneration, a
verification run, or a deploy — and do not restate its contents here.

The three things that must never be got wrong:

1. **Design first.** `contracts/llama-skein.openapi.json` owns the wire contract.
   Edit the spec before any handler or client. Never hand-write a struct that
   duplicates a schema; never edit `pkg/apicontract/llama_skein.gen.go`.
2. **Keep this module buildable.** skein imports
   `github.com/androidand/llama-skein/pkg/apicontract` and resolves it through
   `replace github.com/androidand/llama-skein => ../llama-skein` in its `go.mod`.
   The sibling checkout is authoritative — a compile error here breaks skein's
   build immediately, on any machine, with or without `GOWORK`. Push this repo
   before pushing skein when a change spans both.
3. **Verify before claiming done.** Run the checks and read their output. Two of
   them mislead in ways the skill documents — `go build ./...` needs a generated
   placeholder, and `make check-codegen` reports "stale" for any uncommitted
   generated file.

If a documented command does not work, fix the command — do not substitute a
one-off invocation and leave the next agent with the same trap.

## Tech stack

- Go
- TypeScript, Vite and Svelte 5 for the UI (in `ui-svelte/`)

## Testing

- Name tests `TestProxyManager_<name>`, `TestProcessGroup_<name>`, etc.
- Run new tests with `go test -v -run <pattern>`.
- `gofmt -w <file>` before committing.
- Build binaries into `./build/`.
- `make test-dev` after writing tests (go test + staticcheck) — use when changing
  anything under `proxy/`. Fix every staticcheck error.
- `make test-all` before completing work; includes long concurrency tests.
- `make test-ui` after changing `ui-svelte/`.

## Upstream rebase discipline

- Remote `upstream` → `https://github.com/mostlygeek/llama-swap` (fetch only, never push)
- Remote `origin` → `https://github.com/androidand/llama-skein` (push/pull here)
- Strategy: `git rebase upstream/main` — never merge. Check drift: `make upstream-check`

```bash
git fetch upstream
git log --oneline HEAD..upstream/main    # what upstream has that we don't
git rebase upstream/main
go build ./... && make test-all
git push --force-with-lease origin main
```

**Take from upstream:** GPU fixes, routing/process fixes, new model formats, ROCm
improvements, llama.cpp parser fixes.

**Never take from upstream:** module name changes, or anything conflicting with
`/api/skein/*`, `/api/resources`, `/api/storage`, `/api/config/*`, `/api/models/*`,
or the slot-cancel logic.

**Conflict hotspot:** `proxy/process.go` — our slot-cancel + autoUnload vs upstream
routing changes.

## Fork-specific packages (do not delete or move)

| Path | Purpose |
|------|---------|
| `contracts/llama-skein.openapi.json` | Source-of-truth API spec — edit this first |
| `pkg/apicontract/` | Generated Go types/client (oapi-codegen) — never edit by hand |
| `pkg/api/` | Additional API helpers |
| `internal/server/` | Fork-specific control-plane routes |
| `.claude/skills/skein-dev/` | The ecosystem workflow skill (symlinked into `~/.claude/skills`) |

## Working style

- When summarizing changes, include only details that need further action.
  Say "Done." when there is nothing.
- Use the `gh` CLI for GitHub work.
- Pull requests: short, focused on the change, no test plan, summary written in
  the commit-message style below.

### Commit message format

```
proxy: add new feature

Add new feature that implements functionality X and Y.

- key change 1
- key change 2

fixes #123
```

## Code reviews

- Severity levels High, Medium, Low; label items `H1`, `M2`, `L3`.
- **High** — must fix: security, race conditions, critical bugs.
- **Medium** — recommended: coding style, missing functionality, inconsistencies.
- **Low** — nice to have, nits.
- Include a suggestion with every item. Limit to the three highest-priority items,
  most severe first. Double-check each finding and its remediation before reporting.
