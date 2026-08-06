## Context

llama-skein already has the strongest host-local implementation in the
ecosystem:

- current llama-swap model state, load/unload, `/api/ps`, robust load
  streaming, routing, and performance monitoring;
- llama-skein hardware, storage, exact GGUF metadata, fit, context, offload,
  config, delete, and pull extensions;
- an implemented hypothetical-fit branch for models not yet installed.

The gaps are contract and operation quality. The OpenAPI file does not cover
all implemented lifecycle routes. Pull streams NDJSON on the initiating
request, deletes `.part` on disconnect, cannot reconnect or resume, accepts a
mutable branch URL as identity, and downloads only one path. llmfit handles
operation status and GGUF shard grouping better, while Skein normalizes more
Hugging Face URL shapes.

opencode-skein will own discovery, candidate ranking, cross-host aggregation,
and presentation. llama-skein owns all host-local facts and mutations. Skein
and llmfit are not runtime dependencies.

## Goals / Non-Goals

**Goals:**

- provide one generated, contract-first host model-management API;
- make exact revision-pinned artifact sets the install identity;
- make long downloads observable and cancellable after client reconnect;
- safely resume, verify, atomically install, register, and report artifacts;
- represent sharded GGUF and required auxiliary files as one operation;
- expose consistent model state and operation errors to web and terminal UIs;
- reuse current llama-swap lifecycle behavior instead of replacing it.

**Non-Goals:**

- Hugging Face search, catalog curation, popularity, or task ranking;
- cross-host aggregation or host selection;
- Skein orchestration or Agent placement;
- supporting arbitrary download hosts;
- keeping duplicate handwritten and generated lifecycle contracts.

## Decisions

### 1. OpenAPI is the only cross-repository contract

Inventory, detail, load, unload, remove, pull operation, cancellation, storage
preflight, and operation observation SHALL be defined in
`contracts/llama-skein.openapi.json`. Go and opencode-skein TypeScript clients
are regenerated from it. Handwritten DTOs at these boundaries are removed
after migration.

The contract exposes a capability document so older hosts degrade explicitly.
Clients do not infer support from version strings.

### 2. Separate installation plans from operations

The client submits an immutable installation plan:

```text
ModelInstallPlan
  source_repository
  source_revision
  artifacts[]
    path
    size_bytes
    digest?
    role: weights | projector | tokenizer | config | other
  registration
    model_id
    display_name?
    backend
    flags
    ttl?
```

The server validates and snapshots the plan, then creates a `ModelOperation`
with a host-generated ID. Display names and mutable repository branches are
not artifact identity.

Alternative considered: accept one convenience `owner/repo/file` string.
Rejected as the canonical contract because it cannot safely identify shards,
auxiliary files, or immutable source content.

### 3. Use a host-local operation state machine

```text
queued → preflighting → resolving → downloading → verifying
       → installing → registering → reloading → succeeded
       ↘ cancelled
       ↘ failed
```

Operation records contain current phase, per-artifact and aggregate bytes,
timestamps, terminal error code/message, resulting model ID, and warnings.
`GET` returns a snapshot; an event stream supplies updates. Cancellation is
idempotent. A bounded operation history supports reconnect and diagnostics.

SQLite is not required for the first slice. Operation metadata can be
persisted as small atomic JSON records under llama-skein's owned state
directory while artifact bytes remain in the models directory. Recovery marks
nonterminal operations interrupted and preserves resumable partial files.

Alternative considered: keep progress only in memory like llmfit. Rejected
because reconnect after process restart is a core management expectation.

### 4. Resume and install atomically

Partial files use a deterministic operation/artifact-specific suffix and are
not deleted merely because a client disconnects. A retry:

1. validates the pinned source and existing partial size;
2. requests the remaining range when supported;
3. restarts safely if the origin cannot honor the range;
4. checks available disk for remaining bytes plus safety reserve;
5. verifies declared size and digest when available;
6. fsyncs and renames each artifact atomically;
7. registers the model only after the complete required set is present.

Cancellation removes partials only when explicitly requested by policy. A
separate cleanup operation handles abandoned partials.

### 5. Model shard sets are first-class

Borrow llmfit's tested `NNNNN-of-NNNNN` grouping behavior, but implement it in
Go. The catalog normally supplies the complete artifact set. llama-skein
validates that shard numbering is complete, paths remain within the allowed
directory, and all required files succeed before registration.

### 6. Preserve the upstream lifecycle and state vocabulary

llama-swap's current configured/loading/ready/failed/stopped knowledge remains
the runtime source. llama-skein enriches it with:

- installed-on-disk and configured distinctions;
- exact source/provenance when known;
- fit and hardware observations;
- active model operation;
- failure reason and freshness.

Load uses the upstream router and its robust loading stream. Unload uses the
upstream lifecycle API. Remove unloads first, validates ownership/path, removes
the complete installed artifact set, and removes config in one explicit
operation.

### 7. Validate trust and safety at the host boundary

- only HTTPS Hugging Face hosts and explicit loopback test sources are allowed;
- repository, revision, and paths are encoded independently, never composed
  from an unchecked full path;
- authentication tokens are request secrets and never persisted in operation
  records or logs;
- destination paths are resolved under the configured models directory;
- license and provenance are displayed by opencode-skein, while llama-skein
  records the submitted provenance snapshot;
- destructive operations require an explicit model ID and artifact-set match.

### 8. Upstream adoption is merge-first

llama-skein is currently based on the latest tracked llama-swap upstream
(v223-era). Keep upstream routing, state, load/shutdown robustness, and
performance telemetry. Implement only llama-skein-specific artifact,
operation, and contract behavior around those seams. Generic lifecycle fixes
remain candidates for upstream PRs.

## Risks / Trade-offs

- **Persistent operations add state machinery.** → Keep a small bounded state
  machine and atomic JSON records; do not create a general job framework.
- **Origins may not support ranges or digests.** → Restart safely when ranges
  are unavailable and require size verification; surface weaker verification.
- **Config reload can interrupt active models.** → Register only after install
  and reuse existing no-op/config safety checks.
- **Shard inference can select the wrong siblings.** → Prefer catalog-supplied
  sets and use shard inference only as validated normalization.
- **Breaking pull migration affects Skein callers.** → Regenerate clients,
  migrate opencode-skein, remove or migrate Skein callers, then remove the old
  handwritten route in the same release train.

## Migration Plan

1. Add OpenAPI schemas for inventory, exact install plans, operations, and
   lifecycle endpoints; regenerate clients without changing routing.
2. Implement the operation store/state machine and read-only observation.
3. Move pull execution behind operations with safe partial retention and one
   unsharded GGUF vertical slice.
4. Add resume, cancellation, verification, shard/auxiliary sets, and recovery.
5. Move load/unload/remove to the generated operation contract where
   asynchronous behavior is required.
6. Migrate opencode-skein and any remaining Skein callers.
7. Remove the old connection-bound handwritten pull contract.

Rollback before step 7 can disable operation creation and retain the previous
binary. After step 7, rollback requires restoring a client version compatible
with the old host contract; installed model files remain intact.

## Open Questions

- Which digest sources are sufficiently trustworthy for mandatory
  verification: Hugging Face LFS metadata, HTTP headers, or catalog manifests?
- Should successful operation history expire by count, age, or both?
- Should explicit cancellation retain partials by default for later resume, or
  remove them unless the caller requests retention?
