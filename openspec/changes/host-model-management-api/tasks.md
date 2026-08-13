Sections 1-5 are complete; their completion evidence lives in
`completion-notes.md` (extracted so this change fits GitHub's issue body
limit). Sections 6-7 keep their notes inline.

## 1. Upstream and contract baseline

- [x] 1.1 Record the current llama-swap upstream merge base and verify the
  v223-era model state, load/unload, routing, loading-stream, and performance
  behavior required by this change.
- [x] 1.2 Inventory every implemented llama-skein model inventory, detail,
  fit, storage, pull, load, unload, remove, and config route against the
  OpenAPI source.
- [x] 1.3 Define capability, artifact role, install plan, model operation,
  progress, outcome, and typed error schemas in OpenAPI.
- [x] 1.4 Add generated lifecycle and operation client methods; regenerate Go
  clients and validate the opencode-skein TypeScript generation path.

## 2. Host operation domain

- [x] 2.1 Implement the explicit model-operation state machine and validated
  phase transitions.
- [x] 2.2 Implement bounded atomic operation-record persistence in the owned
  llama-skein state directory.
- [x] 2.3 Add operation create/get/list/event-stream/cancel handlers through
  generated contract types.
- [x] 2.4 Recover interrupted nonterminal operations at startup and expose
  resumable partial-artifact information.
- [x] 2.5 Redact tokens and sensitive request headers from records, logs, and
  errors.

## 3. Exact artifact resolution

- [x] 3.1 Port Skein's tested Hugging Face file-info/blob URL normalization
  into the install-plan resolver without importing Skein.
- [x] 3.2 Port llmfit's tested GGUF shard parsing, grouping, completeness, and
  aggregate-size behavior into Go.
- [x] 3.3 Validate immutable revision, artifact paths, destination
  containment, required roles, size, and optional digest before operation
  creation.
- [x] 3.4 Add disk-space preflight for remaining bytes plus configurable
  safety reserve.
- [x] 3.5 Add table tests for gated repositories, nested files, encoded paths,
  malformed shards, missing auxiliaries, and traversal attempts.

## 4. Resumable installation

- [x] 4.1 Move single-artifact pull execution behind the operation state
  machine while retaining atomic final rename.
- [x] 4.2 Retain deterministic partial files independently of client
  connection lifetime.
- [x] 4.3 Implement safe HTTP range resume with restart fallback when the
  origin does not honor the requested range.
- [x] 4.4 Verify final sizes and available digests before installation.
- [x] 4.5 Download and verify shard/auxiliary sets as one operation and
  register only after all required artifacts succeed.
- [x] 4.6 Implement explicit cancellation policy and abandoned-partial cleanup.
- [x] 4.7 Preserve idempotent registration when artifacts are already complete.

Section 4 (Resumable installation) is now complete: 4.1-4.7 all done,
tested, and pushed individually. The operation.Executor is a real,
working, tested download-verify-install-register pipeline with resume,
digest verification, shard-set correctness, cancellation, and idempotent
re-submission — everything design.md decision 4 asked for except
automatic redispatch on process-restart recovery (explicitly out of scope
per Executor's own doc comment; no task in this list adds it).

## 5. Inventory and lifecycle

- [x] 5.1 Return configured, installed, loading, ready, failed, and unloaded
  state with source, artifact set, active operation, and failure detail.
- [x] 5.2 Route load and unload through current upstream llama-swap lifecycle
  behavior and expose observable terminal outcomes.
- [x] 5.3 Make removal validate artifact ownership, unload affected models,
  remove the full artifact set, and remove configuration explicitly.
- [x] 5.4 Ensure config writes use existing validation/history/no-op safety
  and do not trigger avoidable reloads.

Section 5 (Inventory and lifecycle) is now complete: 5.1-5.4 all done,
tested, and pushed individually. Every gap found across this section
followed the same shape — a pre-existing handler doing less than its own
documentation/schema/design intent already promised — consistent with
what sections 3, 4, and 5 have each turned up at least once.

## 6. Client migration

- [x] 6.1 Regenerate opencode-skein's llama-skein client from the completed
  OpenAPI contract. The documented command could not run: package.json
  referenced `script/build-llama-skein-client.ts`, which `.gitignore`'s
  `script/build-*.ts` had silently swallowed, so the file was never committed.
  Script added and un-ignored (opencode `2b95f0ba7`); client regenerated,
  11 operations -> 39.
- [x] 6.2 Migrate opencode-skein inventory and lifecycle callers to generated
  methods and operation IDs. Inventory (`listModels`) was already on the SDK;
  no handwritten fetches to llama-skein routes remain anywhere in opencode.
  The route move renamed five operations, of which only `patchConfigModel`
  had a caller — and it was pointing at `/api/config/models/{id}`, which this
  change stopped serving. Now `patchModelConfig`.

  Scope check before changing anything: opencode-skein has no load/unload RPC
  callers at all (loading is implicit, triggered server-side by the first
  inference request) and submits no install plans (that is skein's job, 6.3).
  So "lifecycle" here is the model's process state riding on the enriched
  `Model` schema, not a separate RPC surface.

  One handwritten DTO at a generated boundary was found and removed per
  design.md decision 1: `src/local/mdns.ts` declared a `ModelListResult`
  shadow of `ModelList`/`Model` and force-cast `listModels()` onto it, which
  flattened the SDK's discriminated union so the real types never reached the
  call sites. Removing it dropped 47 typecheck errors elsewhere in the package
  (opencode-skein `224a9c9f1e`).

  Not typecheck-clean, and was not before: the package reports 656 errors, 737
  at HEAD with no local changes, including its own generated SDK. The files
  touched here report none.
- [x] 6.3 Inventory Skein pull callers; migrate the still-required autonomous
  placement path and remove gallery-only callers. Six call sites, all reaching
  pull through the `llm.ModelManager` interface shared with Ollama, so the
  migration was contained to the placement path: `providers/pull.go`'s
  SmartPull now builds a `ModelInstallPlan` and follows the operation
  (skein `295097fe6`). No gallery-only callers existed to remove — every site
  is on the CLI/MCP/HTTP lifecycle path. Two behaviour changes: the plan pins
  an immutable commit SHA (the old endpoint used whatever `main` pointed at),
  and registration flags are a token slice, not a command-line string.
- [ ] 6.4 Remove the old connection-bound handwritten pull DTO and route after
  every supported caller has migrated.

## 7. Verification

- [x] 7.1 Unit-test state transitions, persistence, recovery, cancellation,
  redaction, and history expiry. Covered by `transitions_test.go`,
  `store_test.go` (incl. the three `Prune` cases), `recover_test.go`,
  `TestExecutor_Run_StopsWhenAConcurrentCancelRequestTransitionsTheStore`, and
  the `redactionTestToken` cases in `apimodeloperations_test.go`.
  History expiry needed more than a test: `Store.Prune` and
  `CleanupAbandonedPartials` had no production caller at all, so history grew
  unbounded and cancelled downloads leaked their `.part` files. Now called
  from `Server.reclaimOperationStorage` after each operation reaches a
  terminal phase, guarded by three `TestServer_ReclaimOperationStorage_*`
  tests.
- [x] 7.2 Integration-test disconnect/reconnect, process restart, range
  resume, non-range fallback, disk failure, digest mismatch, and cancellation.
  Covered by `executor_test.go`: `ResumesFromAnExistingPartialFileViaRangeRequest`,
  `RestartsWhenOriginIgnoresTheRangeRequest`, `RestartsOn416RangeNotSatisfiable`,
  `FailsOnAMismatchedDigestAndRetainsThePartialFile`,
  `RetainsThePartialFileOnATruncatedDownload`,
  `FailsPreflightBeforeAnyNetworkCall`, plus `recover_test.go` for restart.
- [x] 7.3 Integration-test complete and incomplete multi-shard GGUF installs.
  `TestExecutor_Run_MultiArtifactShardSetAndAuxiliary_HappyPath` and
  `TestExecutor_Run_AbortsTheWholeSetAndNeverRegistersWhenOneArtifactFails`.
- [x] 7.4 Regression-test current llama-swap lifecycle, routing, model-state,
  and loading-stream behavior. The existing `internal/router` and
  `internal/process` suites cover this and stay green: 1537 tests pass across
  32 packages with this change applied.
- [ ] 7.5 Smoke-test one CUDA/ROCm host and one Apple unified-memory host from
  opencode-skein without Skein running. **Blocked on section 6** — there is no
  generated opencode client to smoke-test with until 6.1 lands. Candidate
  hosts are ready and on this build: rocky (ROCm gfx1100) and m3/m5 (Apple).
