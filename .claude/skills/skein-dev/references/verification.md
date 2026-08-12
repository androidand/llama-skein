# Verification

What to run, and what each check actually proves. Read the output — a command
that printed an error still printed it if you did not look.

## llama-skein

```bash
cd ~/dev/llama-skein
make proxy/ui_dist/placeholder.txt   # prerequisite, see below
go build ./...
go vet ./...
go test -short ./...                 # ~1500 tests
make test-all                        # includes long concurrency tests; before finishing work
gofmt -l internal pkg                # must print nothing
```

### `go build ./...` fails on a fresh checkout

```
proxy/ui_embed.go:9:12: pattern ui_dist: no matching files found
```

`proxy/ui_dist/` is generated. `make proxy/ui_dist/placeholder.txt` creates it.
Every `make` test target already depends on it; only a bare `go build`/`go vet`
needs it run first. This is not a broken tree.

### `make check-codegen` fails while you have uncommitted work

The target is:

```make
go generate ./pkg/apicontract
gofmt -w pkg/apicontract/llama_skein.gen.go
git diff --exit-code pkg/apicontract/llama_skein.gen.go || (echo "ERROR: ... is stale" && exit 1)
```

It diffs against **HEAD**, so it fails whenever `llama_skein.gen.go` has
uncommitted changes — including the legitimate case where you just regenerated
it as part of a contract change. It says "stale" either way.

To tell a genuinely stale file from an uncommitted one, regenerate and see
whether anything changed:

```bash
go generate ./pkg/apicontract && gofmt -w pkg/apicontract/llama_skein.gen.go
git diff --stat pkg/apicontract/llama_skein.gen.go   # vs your pre-regen state
```

No change means it was already fresh. The check is only conclusive on a clean
tree — that is what CI runs it on.

## opencode-skein

```bash
cd ~/dev/opencode/packages/opencode
bunx tsc --noEmit -p tsconfig.json
bun run build:llama-skein-client                              # regenerate
bun run script/smoke-llama-skein-client.ts http://<provider>  # verify reachable
```

### Typecheck is not enough

A typecheck proves the client compiles against the contract. It cannot prove
the server serves those routes — a client generated from a spec that has
drifted from the implementation typechecks perfectly and 404s at runtime.

`smoke-llama-skein-client.ts` calls read-only operations against a real
provider and separates three outcomes that look identical to a caller:

- **transport error** — never reached the server; says nothing about the route
- **`404 page not found`** (Go's bare mux response) — the provider does not
  serve this route; this is the failure worth catching
- **`{"src":"llama-skein","error":...}`** — a route that exists, answering

Read-only by design; it must never mutate a live provider. Exits non-zero on
any real failure, so it works in a pipeline.

## skein

```bash
cd ~/dev/skein
go build ./... && go test -short ./...
GOWORK=off go test ./internal/provider/... ./internal/providers/... ./internal/llm/...
```

skein's `go.mod` carries `replace github.com/androidand/llama-skein => ../llama-skein`,
so the **sibling checkout is always authoritative** — the require line is a zero
pseudo-version that only resolves through that replace. Consequences:

- A compile error in your local llama-skein tree breaks skein immediately, with
  or without `GOWORK`. There is no pinned-version fallback to hide behind.
- skein cannot build at all without llama-skein checked out beside it.
- `go get github.com/androidand/llama-skein@<commit>` does nothing useful while
  the replace stands; do not expect a version bump to change what skein compiles.

`~/dev/go.work` lists `llama-skein`, `specsync`, and `spisordning` — not skein.

Verify a contract change against skein by building skein with your llama-skein
tree in place, before pushing either.

## Verifying a deploy

A health check proves *something* is serving, not that your build is. Compare
the reported commit:

```bash
curl -s http://<provider>/api/system/version | python3 -c "import json,sys;print(json.load(sys.stdin)['commit'])"
```

A trailing `+` means a dirty-tree build. `fleet-deploy.sh` does this check
itself (`confirm_rev`) and rolls back on mismatch — it exists because proxmox
reported OK for months while running a binary from a previous release.
