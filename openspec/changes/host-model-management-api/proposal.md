## Why

opencode-skein can already discover every llama-skein host, but llama-skein's
model-management surface is not yet a complete reconnectable contract. Pull
progress is tied to one HTTP response, partial downloads are deleted on
disconnect, multi-file GGUF artifacts are not modeled, and several implemented
lifecycle routes are absent from the OpenAPI source of truth.

The gallery should depend only on opencode-skein and llama-skein. llama-skein
must therefore be a reliable host-local authority for exact artifacts,
inventory, fit, operations, and installation state.

## What Changes

- Put every model inventory and lifecycle endpoint in
  `contracts/llama-skein.openapi.json` and regenerate Go and TypeScript clients.
- Replace connection-bound pull behavior with asynchronous host-local model
  operations that have stable IDs, observable progress, cancellation, and
  terminal outcomes.
- Pin every remote artifact to repository revision and path; retain source,
  size, digest, license/provenance inputs, and registration outcome.
- Add resumable atomic downloads with free-disk preflight, `.part` retention,
  HTTP range support, digest/size verification, and safe final rename.
- Treat sharded GGUF sets and required auxiliary files as one install plan,
  borrowing llmfit's shard grouping behavior.
- Report configured, installed, loading, ready, failed, and unloaded state
  consistently, building on llama-swap's model-state/lifecycle APIs.
- Preserve llama-skein as the only process that writes model files or its host
  configuration; opencode-skein supplies catalog choices and aggregates hosts.
- **BREAKING**: migrate callers from the unversioned streaming pull payload to
  the contract-generated operation API, then remove the duplicate handwritten
  pull contract instead of maintaining aliases.

## Capabilities

### New Capabilities

- `host-model-management`: Contract-first host inventory, exact artifact
  installation, lifecycle operations, progress, cancellation, and recovery.

### Modified Capabilities

None.

## Impact

- OpenAPI source and generated Go/TypeScript clients.
- `internal/server` model, pull, config, storage, and operation handling.
- opencode-skein local-provider and model-gallery clients.
- Existing Skein model-pull callers must migrate or be retired.
- No dependency on Skein, llmfit, or a Rust runtime is introduced.
