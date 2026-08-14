---
name: skein-dev
description: Development workflow for the skein ecosystem (llama-skein, opencode-skein, skein, docs-skein) — the design-first OpenAPI contract flow, code generation, verification, and fleet deploy. Use whenever changing an API route, request/response shape, status code, or error shape in llama-skein; regenerating the Go or TypeScript client; running the build/test/codegen checks; or deploying to providers. Also use when a documented command appears not to work.
---

# skein-dev

The skein ecosystem is four repos with one wire contract between them:

```
skein (supervisor) → opencode-skein (agent runner) → llama-skein (inference proxy)
                                                      ↑ owns the contract
```

| Repo | Path | Role |
|------|------|------|
| llama-skein | `~/dev/llama-skein` | Inference proxy. **Owns `contracts/llama-skein.openapi.json`.** |
| opencode-skein | `~/dev/opencode` | Agent runner. Generated TS client in `packages/opencode/src/local/llama-skein/gen/`. |
| skein | `~/dev/skein` | Supervisor. Consumes generated Go `pkg/apicontract`. |
| docs-skein | `~/dev/docs-skein` | **Private.** Host IPs, deploy scripts, topology. |

## The one rule: design first

`contracts/llama-skein.openapi.json` is the source of truth for every route.
**Edit the spec before touching a handler or a client.** Never hand-write a
struct that duplicates a schema. Never edit `pkg/apicontract/llama_skein.gen.go`.

Full ordered procedure, including what to do when a change moves or renames a
route: **`references/openapi-contract.md`**. Read it before any API change.

Skipping this has repeatedly shipped broken work — a client calling a route the
server stopped serving, docs advertising dead endpoints, generated code drifting
from the spec for months. If you are about to write a handler and have not
edited the spec, stop.

## Verify before you claim done

Do not report success from a command whose output you did not read. The
per-repo commands, what each actually proves, and the checks that mislead are
in **`references/verification.md`**.

The short version for llama-skein:

```bash
make proxy/ui_dist/placeholder.txt   # generated placeholder; plain `go build ./...` fails without it
go build ./... && go vet ./... && go test -short ./...
make check-codegen                   # only meaningful on a clean tree — see the reference
```

## Deploy

`~/dev/docs-skein/deploy/fleet-deploy.sh`, runnable from any Mac in the fleet.
It builds from a clean `origin/main` checkout, so **push before deploying**.
Provider addressing, container/binary names, and the traps are in
**`references/deploy.md`**.

## When a documented command does not work

Fix the command, then the docs — do not route around it. Several commands in
this ecosystem were documented for months while being impossible to run
(a generator script swallowed by `.gitignore`, a `make` target needing an
undocumented prerequisite). If you hit one:

1. Find out why it fails, rather than substituting your own one-off invocation.
2. Fix it so the documented command works.
3. Verify by running exactly the documented command.
4. Only then update the docs.

A one-off invocation that works on your machine leaves the next agent with the
same broken command and no record of the workaround.

## Syncing opencode-skein with upstream

That fork carries ~270 commits of its own, and the danger in a sync is a *clean*
merge that silently drops a fork feature — it has happened twice. The
**`fork-sync`** skill (in opencode-skein) covers the procedure and how to read
`fork:verify`, whose counts distinguish "baseline needs bumping" from "a feature
is gone".

## Docs

`CLAUDE.md` and `AGENTS.md` in each repo point at this skill rather than
restating it. When a workflow changes, change it here — not in four repos.
