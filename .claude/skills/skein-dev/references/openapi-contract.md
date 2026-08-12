# Design-first API changes

`contracts/llama-skein.openapi.json` in llama-skein is the source of truth for
every route, request/response shape, status code, and error shape.

## Order of operations

Do these in order. Each step depends on the one before it.

1. **Edit the spec.** `~/dev/llama-skein/contracts/llama-skein.openapi.json`.
2. **Regenerate Go.**
   ```bash
   cd ~/dev/llama-skein
   go generate ./pkg/apicontract && gofmt -w pkg/apicontract/llama_skein.gen.go
   ```
3. **Implement the handler** using the generated types. Never hand-write a
   struct that duplicates a schema; never edit the `.gen.go` by hand.
4. **Regenerate the TypeScript client.**
   ```bash
   cd ~/dev/opencode/packages/opencode && bun run build:llama-skein-client
   ```
5. **Migrate callers** in opencode and skein. Step 7 tells you which broke.
6. **Verify** — see `verification.md`. Include the client smoke test.
7. **Commit together.** Spec, generated code, handler, and callers in one
   change, so no commit exists where they disagree.

## Renaming or moving a route

The step everyone skips. Changing a path renames the generated operation, which
silently breaks every caller of the old name — a TypeScript build may still pass
if the old method no longer exists but nothing references it, and a Go caller
using a handwritten URL will not fail until runtime.

Before committing a path change, diff the operation sets:

```bash
cd ~/dev/opencode/packages/opencode
grep -oE "public [a-zA-Z]+<ThrowOnError" src/local/llama-skein/gen/sdk.gen.ts \
  | sed 's/public //;s/<ThrowOnError//' | sort > /tmp/new-ops.txt
# stash the regenerated client, repeat against the committed one, then:
comm -23 /tmp/old-ops.txt /tmp/new-ops.txt   # operations that disappeared
```

Every disappeared operation is a potential broken caller. Search for each by
name across opencode and skein before you commit.

Also grep for the old path as a literal — handwritten URLs bypass the client
entirely:

```bash
grep -rn "api/old/path" ~/dev/opencode/packages ~/dev/skein/internal --include="*.ts" --include="*.go"
```

Real example: `/api/config/models/{id}` → `/api/models/config/{id}` renamed
`patchConfigModel` to `patchModelConfig`. opencode's single caller kept
compiling against the stale committed client and called a dead route until the
client was regenerated two months later.

## Client generation details

The generator is `packages/opencode/script/build-llama-skein-client.ts`. It:

- reads the spec from `~/dev/llama-skein/contracts/llama-skein.openapi.json`,
  overridable with `LLAMA_SKEIN_SPEC`;
- resolves `@hey-api/openapi-ts` from `packages/sdk/js`'s pinned copy rather
  than declaring the dependency twice (this fork rebases on upstream, so every
  extra dependency line is conflict surface);
- emits the `LlamaSkeinClient` class — **the class name is part of opencode's
  API**; `mdns.ts` and `httpapi/handlers/local.ts` import it;
- runs the repo's prettier over the output.

Formatting churn: prettier also reformats the bundled `client/` and `core/`
files the generator copies in. That is a one-time ~1500-line diff. If you ever
change generator options, land the formatting change in its own commit or it
will bury the actual contract diff.

## Where generated types must be used

- llama-skein handlers return `pkg/apicontract` types for public control-plane
  endpoints. Do not introduce a local struct that omits schema fields.
- skein imports `github.com/androidand/llama-skein/pkg/apicontract`. Never copy
  or redefine those schemas there, and never generate a second Go client inside
  skein.
- opencode uses `packages/opencode/src/local/llama-skein/gen`. Never mirror the
  schema in handwritten TypeScript.

The Go generator is declared in `pkg/apicontract/doc.go`:

```bash
go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.7.0 \
  -generate types,client -package apicontract \
  -o llama_skein.gen.go ../../contracts/llama-skein.openapi.json
```

## Drift check before committing

```bash
cd ~/dev/llama-skein && git diff -- contracts/llama-skein.openapi.json pkg/apicontract/llama_skein.gen.go
cd ~/dev/opencode    && git diff -- packages/opencode/src/local/llama-skein/gen
cd ~/dev/skein       && git diff -- go.mod go.sum internal
```

If the spec changed but the generated clients did not, stop — you skipped a
regeneration. If handwritten types changed but the spec did not, stop and move
the change into the contract first.

## Checking the spec matches reality

The contract and the server can disagree, and nothing catches it automatically.

```bash
# routes the server serves
grep -oE '"(GET|POST|PATCH|DELETE) /api[^"]*"' ~/dev/llama-skein/internal/server/server.go | sort -u
# routes the contract declares
python3 -c "import json;d=json.load(open('contracts/llama-skein.openapi.json'));[print(m.upper(),p) for p in sorted(d['paths']) for m in d['paths'][p] if m in ('get','post','patch','delete')]" | sort
```

Then confirm against a live provider with the smoke test in `verification.md` —
that is the only check that proves a route is actually reachable.
