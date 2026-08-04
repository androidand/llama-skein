## 1. Upstream and contract baseline

- [x] 1.1 Record the current llama-swap upstream merge base and verify the
  v223-era model state, load/unload, routing, loading-stream, and performance
  behavior required by this change.
      — See `upstream-baseline.md`: merge base is v223, upstream tip is v247
      (66 commits ahead, two — hardware detection and a rocm-smi memory fix —
      worth a deliberate look later). All required behavior verified passing
      at the current merge base; no upstream sync needed to start section 2.
- [x] 1.2 Inventory every implemented llama-skein model inventory, detail,
  fit, storage, pull, load, unload, remove, and config route against the
  OpenAPI source.
      — See `route-inventory.md`: 17 routes already in the contract, 8 are not
      (model detail/delete/load/unload/unload-all/pull/context-recommendation,
      plus the legacy `/unload`) — expected, since formalizing exactly these
      is this change's own purpose. Mapped each to the section that owns it.
- [x] 1.3 Define capability, artifact role, install plan, model operation,
  progress, outcome, and typed error schemas in OpenAPI.
      — Added ArtifactRole, InstallArtifact, ModelRegistration, ModelInstallPlan
      (design.md decision 2), ModelOperationPhase, ModelOperationArtifactProgress
      (progress), ModelOperationError (typed error), ModelOperation (outcome —
      no separate schema needed, it's ModelOperation's terminal shape). Capability
      document reused as-is (Capabilities schema already existed and is generic
      enough). Schemas only, no paths yet — that's 2.3/1.4.
      — Real bug found and fixed along the way: oapi-codegen prunes schemas with
      no path reference by default, silently dropping all of these until
      `skip-prune` was added to doc.go's go:generate line — the workflow
      design.md itself prescribes (schemas before endpoints) doesn't work
      without it. Also hit the exact FitLevel-shaped collision from 1.6 again:
      ModelRegistration.backend's llamacpp/mlx/vllm enum collided with 7
      existing inline duplicates of the same value set (RuntimeInfo,
      RuntimeHealth, Model, ConfigModelRequest, ConfigModelPatchRequest,
      OffloadRecommendation, ModelFit), so oapi-codegen's dedup renamed
      already-relied-upon constants (UpgradeRuntimeParamsBackendLlamacpp →
      bare Llamacpp) despite none of them being touched. Fixed properly this
      time instead of working around it: extracted a shared Backend schema,
      migrated all 8 usages to $ref it, updated the 5 real call sites plus the
      one test file that referenced now-renamed identifiers. Left
      HypotheticalFitRequest/Response's narrower 2-value backend enum alone —
      it's deliberately missing vllm (no hypothetical-fit scoring for it), not
      the same concept.
      — Full go build/vet/test ./... green, opencode-skein TS client
      regenerates and typechecks clean, 86/86 opencode-skein local tests pass.
- [x] 1.4 Add generated lifecycle and operation client methods; regenerate Go
  clients and validate the opencode-skein TypeScript generation path.
      — Added the operation CRUD paths (design.md decisions 2-3): `POST
      /api/models/operations` (submit a plan, create the operation), `GET
      /api/models/operations` (bounded history list), `GET
      /api/models/operations/{id}` (snapshot), `POST
      /api/models/operations/{id}/cancel` (idempotent), `GET
      /api/models/operations/{id}/events` (SSE, each event a ModelOperation
      snapshot — supplementary to the snapshot endpoint, not a replacement:
      a client that missed events resyncs via GET rather than replaying the
      stream). Added ModelOperationList for the list response. Contract only
      — no handlers; that's section 2. No existing-schema collisions this
      time (the Backend consolidation from 1.3 already absorbed the risk).
      go build/vet/test ./... green; opencode-skein TS client regenerates
      with all 5 new methods present and 86/86 opencode-skein local tests
      still pass (one *unrelated* transient typecheck failure observed in
      packages/opencode/src/session/prompt.ts — confirmed caused by a
      concurrent live edit in that repo, not this change: the file's mtime
      matched the exact moment of the typecheck run, and the failure has
      nothing to do with local/llama-skein/gen).

## 2. Host operation domain

- [x] 2.1 Implement the explicit model-operation state machine and validated
  phase transitions.
      — New `internal/operation` package: Phase enum matching design.md
      decision 3's diagram exactly (queued→preflighting→resolving→
      downloading→verifying→installing→registering→reloading→succeeded, any
      non-terminal phase can also go to cancelled/failed), CanTransition
      pure function, Operation.TransitionTo/Fail/Cancel (Cancel is
      idempotent per the API contract, Fail/Cancel both refuse to leave a
      terminal phase). ErrorCode values match ModelOperationError.code
      exactly. Persistence (2.2) and HTTP handlers (2.3) are separate,
      later tasks — this is the pure domain model only. 18 tests, including
      every happy-path step, skip/backward/unknown-phase rejection, and
      terminal-phase immutability. go build/vet/test ./... green.
- [x] 2.2 Implement bounded atomic operation-record persistence in the owned
  llama-skein state directory.
      — internal/operation/store.go: Store.Save/Load/List/Prune, one JSON
      file per operation, temp-file-plus-rename atomic writes (same idiom
      as internal/server/confighelpers.go's and
      proxy/proxymanager_config.go's unexported atomicWriteFile — duplicated
      on purpose rather than shared, to avoid a cross-package dependency for
      three lines of code). DefaultStateDir resolves to
      ~/.llama-skein/operations, alongside the existing
      ~/.llama-skein/skein/profile.json convention (internal/server/server.go)
      rather than inventing a second state-directory root.
      Prune is count-bounded and only ever removes terminal
      (succeeded/cancelled/failed) records — non-terminal ones are never
      pruned, so 2.4's startup recovery always finds every interrupted
      operation. Age-based expiry stays design.md's own open question,
      deliberately not resolved here.
      15 new tests (round-trip, ErrNotFound, atomicity, overwrite, list
      ordering, corrupt-file skip, prune bound/exemption). go build/vet/test
      ./... green (27/27 in internal/operation, full suite otherwise
      unaffected).
- [x] 2.3 Add operation create/get/list/event-stream/cancel handlers through
  generated contract types.
      — internal/server/apimodeloperations.go, wired into the 5 paths added
      in 1.4. Create validates request shape only (empty/missing fields,
      non-positive sizes) — the trust-boundary checks from design.md decision
      7 (HTTPS-only sources, destination containment, already-configured
      model_id collision) land with the actual execution path in a later
      task, once there's somewhere for them to run before download starts.
      Cancel is idempotent and returns 409 (not 404/silent no-op) if the
      operation already reached a *different* terminal phase. Event stream
      is SSE via polling the store every 500ms — no pub/sub exists yet, and
      every operation is also reachable via GET, so a missed/delayed event
      is staleness, not data loss. Server.operationStore is nil-guarded
      (503) rather than a possible panic if ~/.llama-skein/operations
      couldn't be created at startup — logged as a warning there, inference
      still starts.
      18 new handler tests (valid/invalid plan shapes, get/list/cancel
      including idempotency and 404s, stream framing and content-type,
      every handler's 503-when-unavailable path). Full go build/vet/test
      ./... green (one proxy/ test failure observed and confirmed flaky —
      passes in isolation and on a clean full-suite rerun — unrelated to
      this change, a process-spawn timing test under heavy concurrent
      system load).
- [x] 2.4 Recover interrupted nonterminal operations at startup and expose
  resumable partial-artifact information.
      — internal/operation/recover.go: Recover(store, now) finds every
      non-terminal operation (by definition, if it still exists at startup
      no live process is advancing it — that process is the one that just
      exited) and appends an idempotent "interrupted by a server restart
      while at phase X" warning. Phase itself is left unchanged: the phase
      vocabulary has no separate "interrupted" state — phase is which step
      was reached, not whether something is currently working on it right
      now. Wired into server.New() right after the store initializes.
      Honest scope limit, stated plainly rather than papered over:
      "resumable partial-artifact information" here means exposing each
      artifact's last-persisted BytesDownloaded — it does NOT reconcile
      against actual bytes on disk, because no deterministic partial-file
      naming/location scheme exists yet (that's design.md decision 4,
      landing with sections 3-4's actual download execution). Reconciling
      against a naming scheme that doesn't exist yet would be guessing.
      10 new tests (5 operation-package: mark/idempotent/terminal-untouched/
      progress-preserved/empty-store; 1 server-level smoke test exercising
      the real New() wiring, same real-homedir precedent this package
      already accepts for profileStore). Full go build/vet/test ./... green.
- [ ] 2.5 Redact tokens and sensitive request headers from records, logs, and
  errors.

## 3. Exact artifact resolution

- [ ] 3.1 Port Skein's tested Hugging Face file-info/blob URL normalization
  into the install-plan resolver without importing Skein.
- [ ] 3.2 Port llmfit's tested GGUF shard parsing, grouping, completeness, and
  aggregate-size behavior into Go.
- [ ] 3.3 Validate immutable revision, artifact paths, destination
  containment, required roles, size, and optional digest before operation
  creation.
- [ ] 3.4 Add disk-space preflight for remaining bytes plus configurable
  safety reserve.
- [ ] 3.5 Add table tests for gated repositories, nested files, encoded paths,
  malformed shards, missing auxiliaries, and traversal attempts.

## 4. Resumable installation

- [ ] 4.1 Move single-artifact pull execution behind the operation state
  machine while retaining atomic final rename.
- [ ] 4.2 Retain deterministic partial files independently of client
  connection lifetime.
- [ ] 4.3 Implement safe HTTP range resume with restart fallback when the
  origin does not honor the requested range.
- [ ] 4.4 Verify final sizes and available digests before installation.
- [ ] 4.5 Download and verify shard/auxiliary sets as one operation and
  register only after all required artifacts succeed.
- [ ] 4.6 Implement explicit cancellation policy and abandoned-partial cleanup.
- [ ] 4.7 Preserve idempotent registration when artifacts are already complete.

## 5. Inventory and lifecycle

- [ ] 5.1 Return configured, installed, loading, ready, failed, and unloaded
  state with source, artifact set, active operation, and failure detail.
- [ ] 5.2 Route load and unload through current upstream llama-swap lifecycle
  behavior and expose observable terminal outcomes.
- [ ] 5.3 Make removal validate artifact ownership, unload affected models,
  remove the full artifact set, and remove configuration explicitly.
- [ ] 5.4 Ensure config writes use existing validation/history/no-op safety
  and do not trigger avoidable reloads.

## 6. Client migration

- [ ] 6.1 Regenerate opencode-skein's llama-skein client from the completed
  OpenAPI contract.
- [ ] 6.2 Migrate opencode-skein inventory and lifecycle callers to generated
  methods and operation IDs.
- [ ] 6.3 Inventory Skein pull callers; migrate the still-required autonomous
  placement path and remove gallery-only callers.
- [ ] 6.4 Remove the old connection-bound handwritten pull DTO and route after
  every supported caller has migrated.

## 7. Verification

- [ ] 7.1 Unit-test state transitions, persistence, recovery, cancellation,
  redaction, and history expiry.
- [ ] 7.2 Integration-test disconnect/reconnect, process restart, range
  resume, non-range fallback, disk failure, digest mismatch, and cancellation.
- [ ] 7.3 Integration-test complete and incomplete multi-shard GGUF installs.
- [ ] 7.4 Regression-test current llama-swap lifecycle, routing, model-state,
  and loading-stream behavior.
- [ ] 7.5 Smoke-test one CUDA/ROCm host and one Apple unified-memory host from
  opencode-skein without Skein running.
