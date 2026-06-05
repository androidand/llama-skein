# llama-skein

This is **llama-skein**, a fork of [mostlygeek/llama-swap](https://github.com/mostlygeek/llama-swap) extended for the Skein ecosystem. The folder is named `llama-swap/` locally; the Go module and GitHub remote are `github.com/androidand/llama-skein`.

@AGENTS.md
@ECOSYSTEM.md

## Ecosystem position

```
skein supervisor → opencode (agent runner) → llama-skein (inference proxy)
```

llama-skein is the **inference layer**. It sits in front of llama.cpp on each host,
provides a unified OpenAI-compatible API, and exposes control-plane extensions that
skein and opencode depend on.

## Non-negotiable rules

### 1. Design-first — OpenAPI spec owns the wire contract

`contracts/llama-skein.openapi.json` is the source of truth for every API route.

**Always edit the spec BEFORE writing or changing handlers or client code.**

Full workflow — do these in order:
1. Edit `contracts/llama-skein.openapi.json`
2. Regenerate Go types/client in this repo: `go generate ./pkg/apicontract && gofmt -w pkg/apicontract/llama_skein.gen.go`
3. Implement the handler using generated types
4. Regenerate TypeScript client in opencode: `bun run build:llama-skein-client` from `~/dev/opencode/packages/opencode`
5. Update skein callers in `~/dev/skein/internal/` to use new types
6. Commit spec + generated code + implementation + callers together

**Never** hand-write structs that duplicate the OpenAPI schema. Never edit `pkg/apicontract/llama_skein.gen.go` directly.

Verify generated files are current: `make check-codegen`

### 2. Keep this module buildable — skein depends on it

skein imports `github.com/androidand/llama-skein/pkg/apicontract` via a `replace` directive in its `go.mod`. A compile error in this repo breaks `GOWORK=off go build ./...` in skein.

Before every push:
```bash
go build ./...
go test -short ./...
```

Push this repo to origin **before** pushing skein when changes span both repos.

### 3. Upstream rebase discipline

- Remote `upstream` → `https://github.com/mostlygeek/llama-swap` (fetch only)
- Strategy: `git rebase upstream/main` — never merge
- Check drift: `make upstream-check`

**Conflict hotspot**: `proxy/process.go` (our slot-cancel + autoUnload vs upstream routing).

**Never take from upstream**: changes to `/api/skein/*`, `/api/resources`, `/api/storage`, or module name changes.

```bash
git fetch upstream
git log --oneline upstream/main..HEAD   # our commits ahead
git log --oneline HEAD..upstream/main   # upstream new commits
git rebase upstream/main
go build ./... && make test
git push --force-with-lease origin main
```

## Code generation commands

```bash
# From this directory — regenerate Go types/client from OpenAPI spec
go generate ./pkg/apicontract
gofmt -w pkg/apicontract/llama_skein.gen.go

# Check generated files are fresh (no uncommitted diff)
make check-codegen

# From ~/dev/opencode/packages/opencode — regenerate TypeScript client
bun run build:llama-skein-client
```

## Deploying to rocky

See ECOSYSTEM.md for the deploy script. Before deploying: push this branch to origin — the deploy script clones from GitHub, not from local disk.

## git remotes

- **origin** — `https://github.com/androidand/llama-skein` (push/pull here)
- **upstream** — `https://github.com/mostlygeek/llama-swap` (fetch only, never push)

## Fork-specific packages (do not delete or move)

| Path | Purpose |
|------|---------|
| `contracts/llama-skein.openapi.json` | Source-of-truth API spec — edit this first |
| `pkg/apicontract/` | Generated Go types/client (oapi-codegen) — never edit by hand |
| `pkg/api/` | Additional API helpers |
| `internal/server/` | Fork-specific control-plane routes |
