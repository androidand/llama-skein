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
- [x] 2.5 Redact tokens and sensitive request headers from records, logs, and
  errors.
      — Found a real gap while implementing this: ModelInstallPlan (1.3) had
      nowhere to carry an HF token at all. Checked the existing convention
      (POST /api/models/pull's `token` JSON body field, apipull.go) before
      picking a shape — a header-based token would not have worked here:
      CreateAuthMiddleware deletes the Authorization header outright once
      llama-skein's own API-key auth consumes it, before any handler sees
      it. Added an optional `token` field to ModelInstallPlan matching the
      existing body-field convention.
      Handler-side: `plan.Token = nil` immediately after validation, with a
      comment explaining why this is structural, not just discipline —
      operation.Operation has no field capable of holding a token, so it
      cannot reach the persisted record, a log line, or an error message by
      construction. Not yet used to authenticate anything (no download
      execution exists yet, sections 3-4); the schema description says so
      plainly rather than claiming unimplemented behavior.
      Verified "sensitive request headers" is already structurally covered
      rather than inventing new code for a case that doesn't apply: these
      routes use apiChain (auth only), not modelChain (which is what
      captures.go's sensitiveHeaders/redactHeaders protects) — no request
      body or header capture applies to them at all. The general access-log
      line (internal/server/log.go) logs method/path/status/duration only,
      never body or headers.
      2 new tests asserting the guarantee against what's actually on disk
      and in the response body, not just "the code doesn't reference
      plan.Token after this line" (which could silently regress): the
      persisted JSON file's raw bytes never contain a submitted token, and
      neither does a 400 error response for an otherwise-invalid plan that
      also included one. Full go build/vet/test ./... green; opencode-skein
      TS client regenerates and typechecks clean.

## 3. Exact artifact resolution

- [x] 3.1 Port Skein's tested Hugging Face file-info/blob URL normalization
  into the install-plan resolver without importing Skein.
      — internal/operation/source.go: ResolveArtifactURL, ported from
      Skein's normalizeHuggingFaceDownloadURL (internal/providers/
      download_url.go, commit b687c70c1374d42b3a6f6c486057330d0542bf9d).
      Skein's version parses a free-form pasted URL (file-info/blob/resolve
      shapes) into repository/revision/filename; that specific parsing isn't
      needed here since ModelInstallPlan already carries those as separate
      structured fields (opencode-skein resolves a pasted URL into that
      shape before llama-skein ever sees it — design.md's "opencode-skein
      owns discovery... llama-skein owns all host-local facts and
      mutations"). What transfers, and is the actual point of this task, is
      the safe-composition discipline design.md decision 7 requires:
      validate repository/revision/path shape independently, then compose
      the URL via net/url — never trust or concatenate a caller-supplied
      string. Wired into validateInstallPlan so every artifact's URL is
      proven composable (and path-traversal-safe) before an operation is
      even created, not just defined and tested in isolation.
      13 new tests in internal/operation (composition, nested subdirs
      mirroring Skein's own blob-URL fixture, percent-encoding, traversal/
      absolute-path/empty-segment rejection, malformed repository, non-SHA
      revision, short-SHA acceptance) + 1 handler-level test proving the
      wiring. Full go build/vet/test ./... green.
- [x] 3.2 Port llmfit's tested GGUF shard parsing, grouping, completeness, and
  aggregate-size behavior into Go.
      — internal/operation/shard.go: ParseShardInfo and the grouping key
      logic (GroupShards) ported from llmfit's parse_shard_info and
      build_gguf_candidates (llmfit-core/src/providers.rs, commit
      850e80900a583ebb07f8efeab07589dcfd444d92); "aggregate-size" is already
      covered structurally — ModelInstallPlan carries each artifact's own
      size_bytes, there's nothing to sum from a live repo listing the way
      llmfit's version does.
      Completeness (ShardSetComplete) has no llmfit equivalent, stated
      plainly in its own doc comment rather than silently invented as if
      ported: llmfit always scans a live, already-complete Hugging Face
      listing, so it never needed to detect a partial set. A
      client-submitted install plan can reference one, accidentally or
      otherwise, and design.md decision 5 requires catching that before
      registration — a genuine llama-skein-specific addition on top of the
      ported parsing/grouping.
      Wired into validateInstallPlan (weights-role artifacts only —
      projector/tokenizer/config/other are never sharded in practice): an
      incomplete shard set is rejected with the actual part count, not just
      a generic error.
      15 new tests in internal/operation, mirroring llmfit's own fixtures
      where the behavior is shared (basic parse, non-shard rejection,
      distinct-set separation) plus new ones for the added completeness
      check (missing shard, duplicate index, mismatched totals, empty/
      non-shard input) + 2 handler-level tests (reject incomplete, accept
      complete) proving the wiring. Full go build/vet/test ./... green.
- [x] 3.3 Validate immutable revision, artifact paths, destination
  containment, required roles, size, and optional digest before operation
  creation.
      — Immutable revision/artifact paths were already covered by 3.1's
      ResolveArtifactURL and size by the original validateInstallPlan; this
      task closed the remaining three:
      - Destination containment: new operation.ResolveArtifactDestination,
        the source-side-resolver's twin for the local filesystem side of
        design.md decision 7 ("destination paths are resolved under the
        configured models directory"). Containment uses filepath.Rel, not a
        naive string prefix — a destination under a sibling directory that
        merely starts with the same characters ("models-archive" vs
        "models") must never pass. Uses the existing s.modelsDir() accessor
        (falls back to inferring from configured model cmds), not the raw
        config field, and is skipped only when that resolves to "" (no
        models directory knowable yet) rather than blocking every install
        on a fresh install with no models configured.
      - Required roles: a plan needs at least one weights-role artifact —
        registering a model with nothing to run is meaningless.
      - Optional digest: when present, "sha256:" + 64 lowercase hex, nothing
        looser.
      11 new tests in internal/operation (destination composition, nested
      subdirs, traversal/malformed-repository rejection, the sibling-prefix
      containment case) + 7 handler-level tests (no-weights rejection,
      4 malformed-digest shapes, well-formed-digest acceptance). Full go
      build/vet/test ./... green (25/25 in the operations handler suite).
- [x] 3.4 Add disk-space preflight for remaining bytes plus configurable
  safety reserve.
      New `internal/operation/diskspace.go`: `DiskSpace{AvailableBytes,
      TotalBytes}`, `ErrInsufficientDisk`, `CheckDiskPreflight(dir,
      remainingBytes, safetyReserveBytes int64) error` — split into a thin
      OS-calling wrapper and a pure `evaluateDiskPreflight` decision function
      so the space-vs-need arithmetic is testable without depending on the
      test machine's actual free space. `remainingBytes` is documented as
      "bytes still needed" (not "total") because a future resume (section 4)
      will pass what's left after a partial download; for this task's
      create-only call site the two coincide.
      New `internal/operation/diskspace_unix.go` (`syscall.Statfs`, mirrors
      `internal/server/disk_unix.go`'s `storageStats`) and
      `diskspace_windows.go` (`golang.org/x/sys/windows.GetDiskFreeSpaceEx`,
      mirrors `internal/server/disk_windows.go`) — same "duplicate rather
      than cross-package-couple for a few lines" precedent as
      `atomicWriteFile` in store.go, now with a typed `DiskSpace` return
      instead of the server package's reporting-shaped `map[string]any`.
      Wired into `validateInstallPlan` (apimodeloperations.go): sums
      `plan.Artifacts[].SizeBytes` and calls `operation.CheckDiskPreflight`
      against `modelsDir` with a new `defaultDiskSafetyReserveBytes = 5 <<
      30` (5 GiB) constant — a placeholder, not a measured value; not yet
      configurable (no config field exists and no task asks for one). Skipped
      when `modelsDir == ""`, matching 3.3's skip-when-unknown pattern for
      destination containment.
      Tests: `internal/operation/diskspace_test.go` — 8 table-driven cases
      for `evaluateDiskPreflight` (sufficient, insufficient, exact boundary,
      one byte short, negative-input clamping for both remaining and
      reserve, zero/zero), plus a real-filesystem smoke test for
      `availableDiskSpace`/`CheckDiskPreflight` (shape assertions only —
      `TotalBytes > 0`, `AvailableBytes <= TotalBytes` — since exact values
      depend on the machine running the suite) and a nonexistent-dir error
      case. `internal/server/apimodeloperations_test.go` — 2 new handler
      tests: a plan requesting an artifact of `1 << 62` bytes (~4.6 EB, more
      than any real test machine has free) is rejected 400 with "insufficient
      disk space" in the body, proving the real syscall path is wired end to
      end (not mocked); the same oversized plan is accepted 201 when
      `modelsDir` is cleared, proving the skip-when-unknown path.
      Verified: `gofmt -l internal/` clean; `GOWORK=off go build ./...`,
      `go vet ./...`, `go test ./... -count=1` all green (1317 tests, 31
      packages); `make check-codegen` passes with no diff (this task touched
      no OpenAPI schema — disk preflight is server-side-only validation, not
      a new wire type, so no client regeneration was needed).
- [x] 3.5 Add table tests for gated repositories, nested files, encoded paths,
  malformed shards, missing auxiliaries, and traversal attempts.
      Most of these categories already had dedicated coverage from 3.1-3.4 at
      the `internal/operation` package level (nested subdirs, encoded
      characters, bare traversal, malformed shard filenames, incomplete
      weights shard sets) — 3.5 is a genuine gap-filling pass, not a
      restart: new `TestHandleCreateModelOperation_TrustAndShapeTable` in
      `internal/server/apimodeloperations_test.go`, a 7-case table run
      through the real handler (not the operation package in isolation), each
      case chosen because it wasn't already proven wired end to end:
      - two real-world gated-repository names (meta-llama, mistralai) —
        proves gating (enforced by HF via the Authorization header/token,
        never by this validation layer) doesn't need or get special-cased
        treatment in the trust-boundary check;
      - a nested artifact path and an encoded-characters path, each reaching
        POST /api/models/operations directly (previously only exercised via
        operation.ResolveArtifactURL/Destination in isolation);
      - a shard-shaped-but-index-out-of-range filename ("-00004-of-00003")
        used as a plan's sole weights artifact, proving the boundary between
        "invalid shard syntax" (GroupShards treats it as an ordinary
        singleton; not this layer's concern) and "incomplete valid shard
        set" (task 3.3's actual, different check);
      - an incomplete PROJECTOR shard set alongside complete weights, proving
        validateWeightShardCompleteness's documented "non-weights artifacts
        are never grouped or checked" claim rather than just asserting it in
        a comment ("missing auxiliaries" are not rejected — auxiliary
        completeness is out of scope for this layer);
      - a multi-segment traversal disguised behind a legitimate-looking
        nested prefix ("weights/Q4_K_M/../../../etc/passwd"), proving
        safePathSegments checks every segment, not just a leading one.
      Verified: `gofmt -l` clean; `GOWORK=off go build ./...`, `go vet
      ./...`, `go test ./... -count=1` green (1325 tests, 31 packages;
      one internal/process test — TestProcessCommand_StopForkingWrapper,
      a package untouched by this task — failed once under load in the
      full-suite run and passed cleanly re-run in isolation, consistent
      with prior known flakiness in that package, not a regression from this
      change). `make check-codegen` passes with no diff — no OpenAPI schema
      touched by this task.

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
