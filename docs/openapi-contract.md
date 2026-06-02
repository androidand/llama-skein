# OpenAPI Contract Workflow

`contracts/llama-skein.openapi.json` is the source of truth for the llama-skein control API. Work design-first: update the OpenAPI document before changing handlers, Go callers, or TypeScript callers.

## Contract Rules

- Change `contracts/llama-skein.openapi.json` first for any request, response, field, path, status code, or error-shape change.
- Do not hand-write duplicate API structs when generated types already exist.
- Server responses in llama-swap should use generated `pkg/apicontract` types where practical, especially for public control-plane endpoints.
- Skein should consume `github.com/androidand/llama-skein/pkg/apicontract` from the llama-swap/llama-skein module; do not copy or redefine those schemas in Skein.
- opencode should consume generated TypeScript under `packages/opencode/src/local/llama-skein/gen`; do not manually mirror the OpenAPI schema in handwritten TS types.
- If the OpenAPI spec changes, regenerate all affected clients before committing.

## Design-First Sequence

1. Edit `contracts/llama-skein.openapi.json` and describe the desired wire contract.
2. Regenerate the Go client/types in llama-swap.
3. Regenerate the TypeScript client in opencode.
4. Update llama-swap server handlers to return generated contract types.
5. Update Skein callers to use the generated Go package from llama-swap.
6. Update opencode callers to use the generated TS client/types.
7. Add or update tests that assert the wire fields from the generated types.
8. Commit the spec, generated code, implementation, and tests together.

## Generate Go for llama-swap

From `/Users/andreas/dev/llama-swap`:

```bash
go generate ./pkg/apicontract
gofmt -w pkg/apicontract/llama_skein.gen.go
```

The generator is declared in `pkg/apicontract/doc.go`:

```bash
go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.7.0 \
  -generate types,client \
  -package apicontract \
  -o llama_skein.gen.go \
  ../../contracts/llama-skein.openapi.json
```

Generated output:

```text
pkg/apicontract/llama_skein.gen.go
```

## Use Go Contract Types in llama-swap

Use the generated package in server code:

```go
import "github.com/androidand/llama-skein/pkg/apicontract"
```

For example, `/v1/models` should return `apicontract.ModelList` containing `apicontract.Model` entries. Do not introduce local structs that omit fields from the OpenAPI schema.

## Use Go Contract Types in Skein

Skein depends on the llama-swap/llama-skein module and currently has a local development replace to `../llama-swap`. After changing and regenerating llama-swap's Go contract, update Skein callers to import the generated package:

```go
import "github.com/androidand/llama-skein/pkg/apicontract"
```

For local development from `/Users/andreas/dev/skein`, the existing replace makes the sibling checkout authoritative:

```bash
GOWORK=off go test ./internal/provider/... ./internal/providers/... ./internal/llm/...
```

When publishing without the local replace, update Skein to the llama-swap commit that contains the regenerated contract:

```bash
go get github.com/androidand/llama-skein@<commit-or-version>
go mod tidy
```

Do not generate a second, divergent copy of the Go client inside Skein unless the architecture is intentionally changed and documented first.

## Generate TypeScript for opencode

opencode already contains the llama-skein TS generation script. From `/Users/andreas/dev/opencode/packages/opencode`:

```bash
bun run build:llama-skein-client
```

That runs:

```bash
bun run script/build-llama-skein-client.ts
```

The script reads:

```text
/Users/andreas/dev/llama-swap/contracts/llama-skein.openapi.json
```

Generated output:

```text
packages/opencode/src/local/llama-skein/gen/
```

The script uses `@hey-api/openapi-ts` from opencode's SDK workspace dependency and generates TypeScript types plus a fetch client with `LlamaSkeinClient` methods.

## Validation Checklist

Run the narrow generation checks first:

```bash
cd /Users/andreas/dev/llama-swap
go generate ./pkg/apicontract
go test -v ./internal/server
```

```bash
cd /Users/andreas/dev/opencode/packages/opencode
bun run build:llama-skein-client
bun typecheck
```

For Skein callers that use the contract:

```bash
cd /Users/andreas/dev/skein
GOWORK=off go test ./internal/provider/... ./internal/providers/... ./internal/llm/...
```

Then run the broader repo-specific gates required by each repo's `AGENTS.md` before finalizing.

## Drift Checks

Before committing, inspect drift in all three repos:

```bash
cd /Users/andreas/dev/llama-swap
git diff -- contracts/llama-skein.openapi.json pkg/apicontract/llama_skein.gen.go
```

```bash
cd /Users/andreas/dev/opencode
git diff -- packages/opencode/src/local/llama-skein/gen
```

```bash
cd /Users/andreas/dev/skein
git diff -- go.mod go.sum internal
```

If the spec changed but generated clients did not, stop and explain why. If handwritten types changed but the spec did not, stop and move the contract change to OpenAPI first.
