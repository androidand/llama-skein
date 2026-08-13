# Completion notes: host-model-management-api

Completion evidence for sections 1-5, extracted from `tasks.md` so the
change fits GitHub's 65,536-byte issue body limit (it was 71,175). Task
lines and their states stay in `tasks.md`; this file holds the detail of
what was delivered and why. Sections 6-7 keep their notes inline, as their
work is still open.


## 1. Upstream and contract baseline

### 1.1

— See `upstream-baseline.md`: merge base is v223, upstream tip is v247

### 1.1

  (66 commits ahead, two — hardware detection and a rocm-smi memory fix —

### 1.1

  worth a deliberate look later). All required behavior verified passing

### 1.1

  at the current merge base; no upstream sync needed to start section 2.

### 1.2

— See `route-inventory.md`: 17 routes already in the contract, 8 are not

### 1.2

  (model detail/delete/load/unload/unload-all/pull/context-recommendation,

### 1.2

  plus the legacy `/unload`) — expected, since formalizing exactly these

### 1.2

  is this change's own purpose. Mapped each to the section that owns it.

### 1.3

— Added ArtifactRole, InstallArtifact, ModelRegistration, ModelInstallPlan

### 1.3

  (design.md decision 2), ModelOperationPhase, ModelOperationArtifactProgress

### 1.3

  (progress), ModelOperationError (typed error), ModelOperation (outcome —

### 1.3

  no separate schema needed, it's ModelOperation's terminal shape). Capability

### 1.3

  document reused as-is (Capabilities schema already existed and is generic

### 1.3

  enough). Schemas only, no paths yet — that's 2.3/1.4.

### 1.3

— Real bug found and fixed along the way: oapi-codegen prunes schemas with

### 1.3

  no path reference by default, silently dropping all of these until

### 1.3

  `skip-prune` was added to doc.go's go:generate line — the workflow

### 1.3

  design.md itself prescribes (schemas before endpoints) doesn't work

### 1.3

  without it. Also hit the exact FitLevel-shaped collision from 1.6 again:

### 1.3

  ModelRegistration.backend's llamacpp/mlx/vllm enum collided with 7

### 1.3

  existing inline duplicates of the same value set (RuntimeInfo,

### 1.3

  RuntimeHealth, Model, ConfigModelRequest, ConfigModelPatchRequest,

### 1.3

  OffloadRecommendation, ModelFit), so oapi-codegen's dedup renamed

### 1.3

  already-relied-upon constants (UpgradeRuntimeParamsBackendLlamacpp →

### 1.3

  bare Llamacpp) despite none of them being touched. Fixed properly this

### 1.3

  time instead of working around it: extracted a shared Backend schema,

### 1.3

  migrated all 8 usages to $ref it, updated the 5 real call sites plus the

### 1.3

  one test file that referenced now-renamed identifiers. Left

### 1.3

  HypotheticalFitRequest/Response's narrower 2-value backend enum alone —

### 1.3

  it's deliberately missing vllm (no hypothetical-fit scoring for it), not

### 1.3

  the same concept.

### 1.3

— Full go build/vet/test ./... green, opencode-skein TS client

### 1.3

  regenerates and typechecks clean, 86/86 opencode-skein local tests pass.

### 1.4

— Added the operation CRUD paths (design.md decisions 2-3): `POST

### 1.4

  /api/models/operations` (submit a plan, create the operation), `GET

### 1.4

  /api/models/operations` (bounded history list), `GET

### 1.4

  /api/models/operations/{id}` (snapshot), `POST

### 1.4

  /api/models/operations/{id}/cancel` (idempotent), `GET

### 1.4

  /api/models/operations/{id}/events` (SSE, each event a ModelOperation

### 1.4

  snapshot — supplementary to the snapshot endpoint, not a replacement:

### 1.4

  a client that missed events resyncs via GET rather than replaying the

### 1.4

  stream). Added ModelOperationList for the list response. Contract only

### 1.4

— no handlers; that's section 2. No existing-schema collisions this

### 1.4

  time (the Backend consolidation from 1.3 already absorbed the risk).

### 1.4

  go build/vet/test ./... green; opencode-skein TS client regenerates

### 1.4

  with all 5 new methods present and 86/86 opencode-skein local tests

### 1.4

  still pass (one *unrelated* transient typecheck failure observed in

### 1.4

  packages/opencode/src/session/prompt.ts — confirmed caused by a

### 1.4

  concurrent live edit in that repo, not this change: the file's mtime

### 1.4

  matched the exact moment of the typecheck run, and the failure has

### 1.4

  nothing to do with local/llama-skein/gen).

## 2. Host operation domain

### 2.1

— New `internal/operation` package: Phase enum matching design.md

### 2.1

  decision 3's diagram exactly (queued→preflighting→resolving→

### 2.1

  downloading→verifying→installing→registering→reloading→succeeded, any

### 2.1

  non-terminal phase can also go to cancelled/failed), CanTransition

### 2.1

  pure function, Operation.TransitionTo/Fail/Cancel (Cancel is

### 2.1

  idempotent per the API contract, Fail/Cancel both refuse to leave a

### 2.1

  terminal phase). ErrorCode values match ModelOperationError.code

### 2.1

  exactly. Persistence (2.2) and HTTP handlers (2.3) are separate,

### 2.1

  later tasks — this is the pure domain model only. 18 tests, including

### 2.1

  every happy-path step, skip/backward/unknown-phase rejection, and

### 2.1

  terminal-phase immutability. go build/vet/test ./... green.

### 2.2

— internal/operation/store.go: Store.Save/Load/List/Prune, one JSON

### 2.2

  file per operation, temp-file-plus-rename atomic writes (same idiom

### 2.2

  as internal/server/confighelpers.go's and

### 2.2

  proxy/proxymanager_config.go's unexported atomicWriteFile — duplicated

### 2.2

  on purpose rather than shared, to avoid a cross-package dependency for

### 2.2

  three lines of code). DefaultStateDir resolves to

### 2.2

  ~/.llama-skein/operations, alongside the existing

### 2.2

  ~/.llama-skein/skein/profile.json convention (internal/server/server.go)

### 2.2

  rather than inventing a second state-directory root.

### 2.2

  Prune is count-bounded and only ever removes terminal

### 2.2

  (succeeded/cancelled/failed) records — non-terminal ones are never

### 2.2

  pruned, so 2.4's startup recovery always finds every interrupted

### 2.2

  operation. Age-based expiry stays design.md's own open question,

### 2.2

  deliberately not resolved here.

### 2.2

  15 new tests (round-trip, ErrNotFound, atomicity, overwrite, list

### 2.2

  ordering, corrupt-file skip, prune bound/exemption). go build/vet/test

### 2.2

  ./... green (27/27 in internal/operation, full suite otherwise

### 2.2

  unaffected).

### 2.3

— internal/server/apimodeloperations.go, wired into the 5 paths added

### 2.3

  in 1.4. Create validates request shape only (empty/missing fields,

### 2.3

  non-positive sizes) — the trust-boundary checks from design.md decision

### 2.3

  7 (HTTPS-only sources, destination containment, already-configured

### 2.3

  model_id collision) land with the actual execution path in a later

### 2.3

  task, once there's somewhere for them to run before download starts.

### 2.3

  Cancel is idempotent and returns 409 (not 404/silent no-op) if the

### 2.3

  operation already reached a *different* terminal phase. Event stream

### 2.3

  is SSE via polling the store every 500ms — no pub/sub exists yet, and

### 2.3

  every operation is also reachable via GET, so a missed/delayed event

### 2.3

  is staleness, not data loss. Server.operationStore is nil-guarded

### 2.3

  (503) rather than a possible panic if ~/.llama-skein/operations

### 2.3

  couldn't be created at startup — logged as a warning there, inference

### 2.3

  still starts.

### 2.3

  18 new handler tests (valid/invalid plan shapes, get/list/cancel

### 2.3

  including idempotency and 404s, stream framing and content-type,

### 2.3

  every handler's 503-when-unavailable path). Full go build/vet/test

### 2.3

  ./... green (one proxy/ test failure observed and confirmed flaky —

### 2.3

  passes in isolation and on a clean full-suite rerun — unrelated to

### 2.3

  this change, a process-spawn timing test under heavy concurrent

### 2.3

  system load).

### 2.4

— internal/operation/recover.go: Recover(store, now) finds every

### 2.4

  non-terminal operation (by definition, if it still exists at startup

### 2.4

  no live process is advancing it — that process is the one that just

### 2.4

  exited) and appends an idempotent "interrupted by a server restart

### 2.4

  while at phase X" warning. Phase itself is left unchanged: the phase

### 2.4

  vocabulary has no separate "interrupted" state — phase is which step

### 2.4

  was reached, not whether something is currently working on it right

### 2.4

  now. Wired into server.New() right after the store initializes.

### 2.4

  Honest scope limit, stated plainly rather than papered over:

### 2.4

  "resumable partial-artifact information" here means exposing each

### 2.4

  artifact's last-persisted BytesDownloaded — it does NOT reconcile

### 2.4

  against actual bytes on disk, because no deterministic partial-file

### 2.4

  naming/location scheme exists yet (that's design.md decision 4,

### 2.4

  landing with sections 3-4's actual download execution). Reconciling

### 2.4

  against a naming scheme that doesn't exist yet would be guessing.

### 2.4

  10 new tests (5 operation-package: mark/idempotent/terminal-untouched/

### 2.4

  progress-preserved/empty-store; 1 server-level smoke test exercising

### 2.4

  the real New() wiring, same real-homedir precedent this package

### 2.4

  already accepts for profileStore). Full go build/vet/test ./... green.

### 2.5

— Found a real gap while implementing this: ModelInstallPlan (1.3) had

### 2.5

  nowhere to carry an HF token at all. Checked the existing convention

### 2.5

  (POST /api/models/pull's `token` JSON body field, apipull.go) before

### 2.5

  picking a shape — a header-based token would not have worked here:

### 2.5

  CreateAuthMiddleware deletes the Authorization header outright once

### 2.5

  llama-skein's own API-key auth consumes it, before any handler sees

### 2.5

  it. Added an optional `token` field to ModelInstallPlan matching the

### 2.5

  existing body-field convention.

### 2.5

  Handler-side: `plan.Token = nil` immediately after validation, with a

### 2.5

  comment explaining why this is structural, not just discipline —

### 2.5

  operation.Operation has no field capable of holding a token, so it

### 2.5

  cannot reach the persisted record, a log line, or an error message by

### 2.5

  construction. Not yet used to authenticate anything (no download

### 2.5

  execution exists yet, sections 3-4); the schema description says so

### 2.5

  plainly rather than claiming unimplemented behavior.

### 2.5

  Verified "sensitive request headers" is already structurally covered

### 2.5

  rather than inventing new code for a case that doesn't apply: these

### 2.5

  routes use apiChain (auth only), not modelChain (which is what

### 2.5

  captures.go's sensitiveHeaders/redactHeaders protects) — no request

### 2.5

  body or header capture applies to them at all. The general access-log

### 2.5

  line (internal/server/log.go) logs method/path/status/duration only,

### 2.5

  never body or headers.

### 2.5

  2 new tests asserting the guarantee against what's actually on disk

### 2.5

  and in the response body, not just "the code doesn't reference

### 2.5

  plan.Token after this line" (which could silently regress): the

### 2.5

  persisted JSON file's raw bytes never contain a submitted token, and

### 2.5

  neither does a 400 error response for an otherwise-invalid plan that

### 2.5

  also included one. Full go build/vet/test ./... green; opencode-skein

### 2.5

  TS client regenerates and typechecks clean.

## 3. Exact artifact resolution

### 3.1

— internal/operation/source.go: ResolveArtifactURL, ported from

### 3.1

  Skein's normalizeHuggingFaceDownloadURL (internal/providers/

### 3.1

  download_url.go, commit b687c70c1374d42b3a6f6c486057330d0542bf9d).

### 3.1

  Skein's version parses a free-form pasted URL (file-info/blob/resolve

### 3.1

  shapes) into repository/revision/filename; that specific parsing isn't

### 3.1

  needed here since ModelInstallPlan already carries those as separate

### 3.1

  structured fields (opencode-skein resolves a pasted URL into that

### 3.1

  shape before llama-skein ever sees it — design.md's "opencode-skein

### 3.1

  owns discovery... llama-skein owns all host-local facts and

### 3.1

  mutations"). What transfers, and is the actual point of this task, is

### 3.1

  the safe-composition discipline design.md decision 7 requires:

### 3.1

  validate repository/revision/path shape independently, then compose

### 3.1

  the URL via net/url — never trust or concatenate a caller-supplied

### 3.1

  string. Wired into validateInstallPlan so every artifact's URL is

### 3.1

  proven composable (and path-traversal-safe) before an operation is

### 3.1

  even created, not just defined and tested in isolation.

### 3.1

  13 new tests in internal/operation (composition, nested subdirs

### 3.1

  mirroring Skein's own blob-URL fixture, percent-encoding, traversal/

### 3.1

  absolute-path/empty-segment rejection, malformed repository, non-SHA

### 3.1

  revision, short-SHA acceptance) + 1 handler-level test proving the

### 3.1

  wiring. Full go build/vet/test ./... green.

### 3.2

— internal/operation/shard.go: ParseShardInfo and the grouping key

### 3.2

  logic (GroupShards) ported from llmfit's parse_shard_info and

### 3.2

  build_gguf_candidates (llmfit-core/src/providers.rs, commit

### 3.2

  850e80900a583ebb07f8efeab07589dcfd444d92); "aggregate-size" is already

### 3.2

  covered structurally — ModelInstallPlan carries each artifact's own

### 3.2

  size_bytes, there's nothing to sum from a live repo listing the way

### 3.2

  llmfit's version does.

### 3.2

  Completeness (ShardSetComplete) has no llmfit equivalent, stated

### 3.2

  plainly in its own doc comment rather than silently invented as if

### 3.2

  ported: llmfit always scans a live, already-complete Hugging Face

### 3.2

  listing, so it never needed to detect a partial set. A

### 3.2

  client-submitted install plan can reference one, accidentally or

### 3.2

  otherwise, and design.md decision 5 requires catching that before

### 3.2

  registration — a genuine llama-skein-specific addition on top of the

### 3.2

  ported parsing/grouping.

### 3.2

  Wired into validateInstallPlan (weights-role artifacts only —

### 3.2

  projector/tokenizer/config/other are never sharded in practice): an

### 3.2

  incomplete shard set is rejected with the actual part count, not just

### 3.2

  a generic error.

### 3.2

  15 new tests in internal/operation, mirroring llmfit's own fixtures

### 3.2

  where the behavior is shared (basic parse, non-shard rejection,

### 3.2

  distinct-set separation) plus new ones for the added completeness

### 3.2

  check (missing shard, duplicate index, mismatched totals, empty/

### 3.2

  non-shard input) + 2 handler-level tests (reject incomplete, accept

### 3.2

  complete) proving the wiring. Full go build/vet/test ./... green.

### 3.3

— Immutable revision/artifact paths were already covered by 3.1's

### 3.3

  ResolveArtifactURL and size by the original validateInstallPlan; this

### 3.3

  task closed the remaining three:

### 3.3

  - Destination containment: new operation.ResolveArtifactDestination,

### 3.3

  the source-side-resolver's twin for the local filesystem side of

### 3.3

  design.md decision 7 ("destination paths are resolved under the

### 3.3

  configured models directory"). Containment uses filepath.Rel, not a

### 3.3

  naive string prefix — a destination under a sibling directory that

### 3.3

  merely starts with the same characters ("models-archive" vs

### 3.3

  "models") must never pass. Uses the existing s.modelsDir() accessor

### 3.3

  (falls back to inferring from configured model cmds), not the raw

### 3.3

  config field, and is skipped only when that resolves to "" (no

### 3.3

  models directory knowable yet) rather than blocking every install

### 3.3

  on a fresh install with no models configured.

### 3.3

  - Required roles: a plan needs at least one weights-role artifact —

### 3.3

  registering a model with nothing to run is meaningless.

### 3.3

  - Optional digest: when present, "sha256:" + 64 lowercase hex, nothing

### 3.3

  looser.

### 3.3

  11 new tests in internal/operation (destination composition, nested

### 3.3

  subdirs, traversal/malformed-repository rejection, the sibling-prefix

### 3.3

  containment case) + 7 handler-level tests (no-weights rejection,

### 3.3

  4 malformed-digest shapes, well-formed-digest acceptance). Full go

### 3.3

  build/vet/test ./... green (25/25 in the operations handler suite).

### 3.4

  New `internal/operation/diskspace.go`: `DiskSpace{AvailableBytes,

### 3.4

  TotalBytes}`, `ErrInsufficientDisk`, `CheckDiskPreflight(dir,

### 3.4

  remainingBytes, safetyReserveBytes int64) error` — split into a thin

### 3.4

  OS-calling wrapper and a pure `evaluateDiskPreflight` decision function

### 3.4

  so the space-vs-need arithmetic is testable without depending on the

### 3.4

  test machine's actual free space. `remainingBytes` is documented as

### 3.4

  "bytes still needed" (not "total") because a future resume (section 4)

### 3.4

  will pass what's left after a partial download; for this task's

### 3.4

  create-only call site the two coincide.

### 3.4

  New `internal/operation/diskspace_unix.go` (`syscall.Statfs`, mirrors

### 3.4

  `internal/server/disk_unix.go`'s `storageStats`) and

### 3.4

  `diskspace_windows.go` (`golang.org/x/sys/windows.GetDiskFreeSpaceEx`,

### 3.4

  mirrors `internal/server/disk_windows.go`) — same "duplicate rather

### 3.4

  than cross-package-couple for a few lines" precedent as

### 3.4

  `atomicWriteFile` in store.go, now with a typed `DiskSpace` return

### 3.4

  instead of the server package's reporting-shaped `map[string]any`.

### 3.4

  Wired into `validateInstallPlan` (apimodeloperations.go): sums

### 3.4

  `plan.Artifacts[].SizeBytes` and calls `operation.CheckDiskPreflight`

### 3.4

  against `modelsDir` with a new `defaultDiskSafetyReserveBytes = 5 <<

### 3.4

  30` (5 GiB) constant — a placeholder, not a measured value; not yet

### 3.4

  configurable (no config field exists and no task asks for one). Skipped

### 3.4

  when `modelsDir == ""`, matching 3.3's skip-when-unknown pattern for

### 3.4

  destination containment.

### 3.4

  Tests: `internal/operation/diskspace_test.go` — 8 table-driven cases

### 3.4

  for `evaluateDiskPreflight` (sufficient, insufficient, exact boundary,

### 3.4

  one byte short, negative-input clamping for both remaining and

### 3.4

  reserve, zero/zero), plus a real-filesystem smoke test for

### 3.4

  `availableDiskSpace`/`CheckDiskPreflight` (shape assertions only —

### 3.4

  `TotalBytes > 0`, `AvailableBytes <= TotalBytes` — since exact values

### 3.4

  depend on the machine running the suite) and a nonexistent-dir error

### 3.4

  case. `internal/server/apimodeloperations_test.go` — 2 new handler

### 3.4

  tests: a plan requesting an artifact of `1 << 62` bytes (~4.6 EB, more

### 3.4

  than any real test machine has free) is rejected 400 with "insufficient

### 3.4

  disk space" in the body, proving the real syscall path is wired end to

### 3.4

  end (not mocked); the same oversized plan is accepted 201 when

### 3.4

  `modelsDir` is cleared, proving the skip-when-unknown path.

### 3.4

  Verified: `gofmt -l internal/` clean; `GOWORK=off go build ./...`,

### 3.4

  `go vet ./...`, `go test ./... -count=1` all green (1317 tests, 31

### 3.4

  packages); `make check-codegen` passes with no diff (this task touched

### 3.4

  no OpenAPI schema — disk preflight is server-side-only validation, not

### 3.4

  a new wire type, so no client regeneration was needed).

### 3.5

  Most of these categories already had dedicated coverage from 3.1-3.4 at

### 3.5

  the `internal/operation` package level (nested subdirs, encoded

### 3.5

  characters, bare traversal, malformed shard filenames, incomplete

### 3.5

  weights shard sets) — 3.5 is a genuine gap-filling pass, not a

### 3.5

  restart: new `TestHandleCreateModelOperation_TrustAndShapeTable` in

### 3.5

  `internal/server/apimodeloperations_test.go`, a 7-case table run

### 3.5

  through the real handler (not the operation package in isolation), each

### 3.5

  case chosen because it wasn't already proven wired end to end:

### 3.5

  - two real-world gated-repository names (meta-llama, mistralai) —

### 3.5

  proves gating (enforced by HF via the Authorization header/token,

### 3.5

  never by this validation layer) doesn't need or get special-cased

### 3.5

  treatment in the trust-boundary check;

### 3.5

  - a nested artifact path and an encoded-characters path, each reaching

### 3.5

  POST /api/models/operations directly (previously only exercised via

### 3.5

  operation.ResolveArtifactURL/Destination in isolation);

### 3.5

  - a shard-shaped-but-index-out-of-range filename ("-00004-of-00003")

### 3.5

  used as a plan's sole weights artifact, proving the boundary between

### 3.5

  "invalid shard syntax" (GroupShards treats it as an ordinary

### 3.5

  singleton; not this layer's concern) and "incomplete valid shard

### 3.5

  set" (task 3.3's actual, different check);

### 3.5

  - an incomplete PROJECTOR shard set alongside complete weights, proving

### 3.5

  validateWeightShardCompleteness's documented "non-weights artifacts

### 3.5

  are never grouped or checked" claim rather than just asserting it in

### 3.5

  a comment ("missing auxiliaries" are not rejected — auxiliary

### 3.5

  completeness is out of scope for this layer);

### 3.5

  - a multi-segment traversal disguised behind a legitimate-looking

### 3.5

  nested prefix ("weights/Q4_K_M/../../../etc/passwd"), proving

### 3.5

  safePathSegments checks every segment, not just a leading one.

### 3.5

  Verified: `gofmt -l` clean; `GOWORK=off go build ./...`, `go vet

### 3.5

  ./...`, `go test ./... -count=1` green (1325 tests, 31 packages;

### 3.5

  one internal/process test — TestProcessCommand_StopForkingWrapper,

### 3.5

  a package untouched by this task — failed once under load in the

### 3.5

  full-suite run and passed cleanly re-run in isolation, consistent

### 3.5

  with prior known flakiness in that package, not a regression from this

### 3.5

  change). `make check-codegen` passes with no diff — no OpenAPI schema

### 3.5

  touched by this task.

## 4. Resumable installation

### 4.1

  New `internal/operation/executor.go`: `Executor` (Store, ModelsDir,

### 4.1

  Client, SafetyReserveBytes, Register, Now, Logf) and `Run(ctx, op)`,

### 4.1

  driving an operation from preflighting through

### 4.1

  resolving/downloading/verifying/installing/registering/reloading to

### 4.1

  succeeded/failed, saving to Store after every phase change and every

### 4.1

  ~10 MiB during download. Real HTTP download (not simulated), atomic

### 4.1

  final rename (fsync during download's Close, os.Rename during

### 4.1

  install), deterministic `.part` partial naming matching the

### 4.1

  pre-existing internal/server/apipull.go convention. `Registrar` is an

### 4.1

  injected callback (`func(op, weightsPath) error`) so this package

### 4.1

  doesn't depend on internal/server for config-write/reload — the

### 4.1

  dependency already runs the other way.

### 4.1

  Extended `operation.Operation`/`ArtifactProgress` with the fields

### 4.1

  execution needs that plan-acceptance alone didn't require: `Role`,

### 4.1

  `Digest` per artifact, and a `Registration` snapshot (ModelID,

### 4.1

  DisplayName, Backend, Flags, TTL) captured once at accept time

### 4.1

  (design.md decision 2: the plan, registration included, is immutable

### 4.1

  once accepted). Added `operation.NewFromPlan(Plan, now)` as a new

### 4.1

  convenience constructor; `New()` itself is untouched and still used

### 4.1

  directly by all ~100 pre-4.1 tests that don't need a full Plan.

### 4.1

  `internal/operation/store.go`'s `record` type extended with

### 4.1

  `Registration` (omitzero, so old on-disk records decode fine).

### 4.1

  Wired into `internal/server`: `handleAPICreateModelOperation`

### 4.1

  converts the accepted `apicontract.ModelInstallPlan` to

### 4.1

  `operation.Plan` (`toOperationPlan`), builds the operation via

### 4.1

  `NewFromPlan`, and — after writing the 201 response, never before —

### 4.1

  dispatches `Server.runOperation` in a goroutine. `runOperation` is a

### 4.1

  new `Server` field, nil by default (same "nil field is a no-op"

### 4.1

  pattern `reloadFn`/`triggerReload` already uses) so every

### 4.1

  pre-existing operations handler test keeps making zero real network

### 4.1

  calls; `New()` wires it to the real executor for production.

### 4.1

  `registerInstalledModel` is the `Registrar`: writes config via the

### 4.1

  same `writeModelToConfig` path `apipull.go`'s `registerPulledModel`

### 4.1

  uses, then calls `triggerReload()`. New `Server.operationHTTPClient`

### 4.1

  field (nil → `http.DefaultClient`) lets a test point execution at a

### 4.1

  local `httptest.Server` instead of huggingface.co.

### 4.1

  Explicitly NOT done in this task (each is a later 4.x task, listed

### 4.1

  here so the gap is never silently assumed covered): no HTTP range

### 4.1

  resume (4.3, a failed/restarted download always restarts from byte

### 4.1

  0); no digest verification (4.4, only declared size is checked —

### 4.1

  `Artifact.Digest` is carried but unread); no shard/auxiliary-set-aware

### 4.1

  install ordering (4.5, multiple artifacts download fine but nothing

### 4.1

  groups shards specially); no cooperative mid-flight cancellation

### 4.1

  (4.6, Run only stops early if its context is cancelled — a

### 4.1

  concurrent `/cancel` request against the same operation ID doesn't

### 4.1

  interrupt an already-running Run because it doesn't reload the

### 4.1

  record from Store mid-flight); no idempotent short-circuit for an

### 4.1

  already-complete artifact set (4.7, Run always redownloads).

### 4.1

  Tests: `internal/operation/executor_test.go`, 7 new tests against a

### 4.1

  real `httptest.Server` reached via a request-rewriting

### 4.1

  `http.RoundTripper` (never huggingface.co) — happy path (download,

### 4.1

  atomic install, `.part` cleanup, correct `BytesDownloaded`,

### 4.1

  persisted terminal record), registration skipped when `Register` is

### 4.1

  nil, HTTP error, size-mismatch verify failure (partial retained, no

### 4.1

  final file), disk-preflight failure with a request-count assertion

### 4.1

  proving no network call happens first, a truncated-download failure

### 4.1

  (partial retained with exactly the bytes actually received), and a

### 4.1

  Register-callback failure (artifact still installed on disk despite

### 4.1

  the registration failure, since install happens before registering

### 4.1

  in phase order). `internal/server/apimodeloperations_executor_test.go`,

### 4.1

  1 new end-to-end test through the real HTTP handler proving the full

### 4.1

  chain (create → download → install → config write → reload trigger)

### 4.1

  wired correctly, synchronized by polling the persisted operation

### 4.1

  record for a terminal phase (not a sleep or a WaitGroup racing a

### 4.1

  different goroutine — an earlier draft of this test raced on

### 4.1

  `reloadFn`'s own goroutine and was caught and fixed before landing).

### 4.1

  Verified: `gofmt -l` clean; `GOWORK=off go build ./...`, `go vet

### 4.1

  ./...`, `go test ./... -count=1` green (1333 tests, 31 packages);

### 4.1

  `go test ./internal/operation/... ./internal/server/... -race

### 4.1

  -count=1` also green (413 tests, no race detected across the new

### 4.1

  goroutine dispatch). `make check-codegen` passes with no diff — no

### 4.1

  OpenAPI schema touched by this task.

### 4.2

  Closed out by 4.3, not a separate implementation: 4.1 already

### 4.2

  provided the mechanism (Run executes on `s.shutdownCtx`, never

### 4.2

  `r.Context()`, so client disconnect never interrupts or deletes a

### 4.2

  partial) and 4.3 is what makes the retained file matter in

### 4.2

  practice — `downloadOne` now actually reads a retained `.part` file's

### 4.2

  real on-disk size and resumes from it (see 4.3 below), rather than

### 4.2

  the file just sitting there unused. Left unchecked after 4.1

### 4.2

  specifically because the file had nothing to be resumed by yet;

### 4.2

  checked now that it does.

### 4.3

  `internal/operation/executor.go`'s `downloadOne` re-stats the

### 4.3

  artifact's `.part` file (not `op.Artifacts[index].BytesDownloaded`,

### 4.3

  which is only updated every ~10 MiB and could undercount after a

### 4.3

  crash — the file's actual on-disk size is authoritative) and:

### 4.3

  1. if it's already at or past the declared size, skips the network

### 4.3

  entirely (a prior attempt finished downloading but crashed before

### 4.3

  verify/install ran);

### 4.3

  2. otherwise requests `Range: bytes=<existingSize>-` via new

### 4.3

  `fetchArtifact`/`doGet` helpers;

### 4.3

  3. on `206 Partial Content`, appends to the existing file from where

### 4.3

  it left off;

### 4.3

  4. on `200 OK` or `416 Range Not Satisfiable` (design.md decision 4

### 4.3

  point 3: "restarts safely if the origin cannot honor the range"),

### 4.3

  records a warning on the operation (matching the OpenAPI

### 4.3

  `range_unsupported` error code's own doc comment: this is not

### 4.3

  itself a terminal failure) and reissues a plain GET, truncating

### 4.3

  and restarting the artifact from byte 0 — the stale partial's

### 4.3

  bytes are discarded, never left mixed into the result.

### 4.3

  Checked whether this change list adds a client-facing retry API or an

### 4.3

  auto-resume-on-recovery trigger before finalizing scope: it does not

### 4.3

  (no "retry" path exists anywhere in contracts/llama-skein.openapi.json,

### 4.3

  and `operation.Recover`, task 2.4, only marks an interrupted operation,

### 4.3

  it never redispatches `Run`). So resumability here is a capability

### 4.3

  `Run`/`downloadOne` now has, exercised directly by tests that pre-seed

### 4.3

  a `.part` file before calling `Run` — not yet something that happens

### 4.3

  automatically after a process restart. That gap is called out

### 4.3

  explicitly in `Executor`'s doc comment rather than left implicit.

### 4.3

  Tests: 4 new cases in `internal/operation/executor_test.go` — a

### 4.3

  real resume via `206` (asserts the actual `Range` header sent, and

### 4.3

  that no warning is recorded on a clean resume), a `200`-ignores-range

### 4.3

  restart (asserts exactly 2 requests happened and the stale partial's

### 4.3

  bytes don't leak into the final content), a `416` restart (same

### 4.3

  shape, different rejection status), and the "already fully

### 4.3

  downloaded" shortcut (asserts zero network requests via a handler

### 4.3

  that calls `t.Fatal` if hit at all). Caught and fixed a bug in my own

### 4.3

  first draft of the 416 test before landing: its stale-partial fixture

### 4.3

  was accidentally *longer* than the artifact's declared size, so it

### 4.3

  silently exercised the "already complete" shortcut instead of the

### 4.3

  416 path at all — verify's real digest-mismatch failure on that

### 4.3

  draft is what surfaced the mistake.

### 4.3

  Verified: `gofmt -l` clean; `GOWORK=off go build ./...`, `go vet

### 4.3

  ./...`, `go test ./... -count=1` green (1337 tests, 31 packages);

### 4.3

  `go test ./internal/operation/... ./internal/server/... -race

### 4.3

  -count=1` green (417 tests). `make check-codegen` passes with no

### 4.3

  diff — no OpenAPI schema touched by this task.

### 4.4

  `internal/operation/executor.go`'s `verify` (size check, from 4.1)

### 4.4

  now also calls new `verifyDigest(path, want string) error` for every

### 4.4

  artifact whose plan supplied a `Digest`: streams the `.part` file

### 4.4

  through `crypto/sha256` (not read into memory — these are GGUF

### 4.4

  weight files, often tens of gigabytes) and compares hex output

### 4.4

  against the declared `"sha256:<hex>"` string, case-insensitively.

### 4.4

  A mismatch fails the operation with `ErrorDigestMismatch` (same code

### 4.4

  the size check already used — the OpenAPI error enum has no separate

### 4.4

  code, per design.md decision 4 point 5 grouping size+digest under one

### 4.4

  verification step) and, matching every other verify failure, leaves

### 4.4

  the `.part` file in place rather than deleting it.

### 4.4

  When an artifact has no `Digest` (`InstallArtifact.digest`'s own doc

### 4.4

  comment: "a missing digest is reported as weaker verification, not

### 4.4

  rejected outright"), the artifact is still accepted on size alone,

### 4.4

  but a warning is now recorded on the operation

### 4.4

  (`"<path>: verified by size only; no digest was provided"`) rather

### 4.4

  than silently treating size-only verification as equivalent to a

### 4.4

  real digest check.

### 4.4

  Tests: 4 new cases in `internal/operation/executor_test.go` — a

### 4.4

  matching digest (no warning), a mismatched digest (operation fails,

### 4.4

  `.part` retained with the actual wrong-but-real bytes, same pattern

### 4.4

  as 4.1's size-mismatch test), the no-digest-provided path (exactly

### 4.4

  one size-only warning), and `verifyDigest` rejecting a non-"sha256:"

### 4.4

  form directly. Updated the three 4.3 range-resume tests' warning-

### 4.4

  count assertions, which had been written assuming zero verification

### 4.4

  warnings existed yet — none of those tests' plans set `Digest`, so

### 4.4

  each now also picks up the new size-only warning alongside whatever

### 4.4

  it was already asserting about range-resume warnings.

### 4.4

  Verified: `gofmt -l` clean; `GOWORK=off go build ./...`, `go vet

### 4.4

  ./...`, `go test ./... -count=1` green (1341 tests, 31 packages);

### 4.4

  `go test ./internal/operation/... ./internal/server/... -race

### 4.4

  -count=1` green (421 tests). `make check-codegen` passes with no

### 4.4

  diff — no OpenAPI schema touched by this task.

### 4.5

  Most of this requirement was already structurally true since 4.1:

### 4.5

  `download`/`verify`/`install` each loop over every artifact in the

### 4.5

  operation and return on the first failure, so `Run` never reaches

### 4.5

  `PhaseRegistering` — and therefore never calls `Register` — unless

### 4.5

  every artifact in the set succeeded through download, verify, and

### 4.5

  install. The one genuine gap this task closed: `primaryWeightsPath`

### 4.5

  (which artifact `Registrar` points a backend command at) picked

### 4.5

  "whichever weights-role artifact plan.artifacts listed first," which

### 4.5

  is only correct by coincidence for a client that happens to submit

### 4.5

  shards in index order — nothing enforces that (task 3.3's

### 4.5

  `validateWeightShardCompleteness` only checks a complete set is

### 4.5

  present, not that it's sorted). Fixed to use `ParseShardInfo`

### 4.5

  (task 3.2) and pick the lowest-indexed shard among the weights-role

### 4.5

  artifacts specifically — llama.cpp's own convention is to be pointed

### 4.5

  at shard 1 and auto-discover the rest via the `-NNNNN-of-NNNNN`

### 4.5

  filename convention.

### 4.5

  Tests: 2 new cases in `internal/operation/executor_test.go` — a

### 4.5

  2-shard set plus a tokenizer auxiliary file, submitted in the plan

### 4.5

  with shard 2 listed *before* shard 1 specifically to prove

### 4.5

  `primaryWeightsPath` doesn't trust plan order, asserting the

### 4.5

  registered path is shard 1 and that all three files install with

### 4.5

  correct content; and a failure case where shard 2 of 2 404s,

### 4.5

  asserting `Register` is never called (via a callback that fails the

### 4.5

  test if invoked) even though shard 1 downloaded successfully and is

### 4.5

  left on disk as a retained partial, per design.md decision 4's

### 4.5

  partial-retention guarantee.

### 4.5

  Verified: `gofmt -l` clean; `GOWORK=off go build ./...`, `go vet

### 4.5

  ./...`, `go test ./... -count=1` green (1343 tests, 31 packages);

### 4.5

  `go test ./internal/operation/... ./internal/server/... -race

### 4.5

  -count=1` green (423 tests). `make check-codegen` passes with no

### 4.5

  diff — no OpenAPI schema touched by this task. Also fixed an

### 4.5

  accidental duplicate "4.5" line in this file left over from an

### 4.5

  earlier edit, found while updating this entry.

### 4.6

  Found and fixed a real bug while implementing this, not just added

### 4.6

  new behavior: POST /api/models/operations/{id}/cancel

### 4.6

  (handleAPICancelModelOperation, task 2.3) already correctly loaded,

### 4.6

  transitioned, and saved its own `*Operation` instance — but

### 4.6

  `Executor.Run` (task 4.1) held a completely separate instance and had

### 4.6

  no way to notice, so its next periodic progress save would silently

### 4.6

  overwrite the cancelled record with its own stale phase. Confirmed

### 4.6

  via a flaking new test before the fix landed (see below).

### 4.6

  Cancellation policy (`internal/operation/executor.go`): `Run` now

### 4.6

  checks for a concurrent cancellation cooperatively — at every phase

### 4.6

  transition (`advance`, via new `stopIfCancelled`) and periodically

### 4.6

  during an in-progress download (`downloadOne`'s loop, every

### 4.6

  `ProgressSaveIntervalBytes`, a new `Executor` field defaulting to

### 4.6

  10 MiB). This bounds how quickly cancellation takes effect by that

### 4.6

  interval; per the OpenAPI cancel endpoint's own description

### 4.6

  ("the operation transitions to 'cancelled' asynchronously"), that's

### 4.6

  the documented contract, not a shortcut. New `errExternallyCancelled`

### 4.6

  sentinel (compared by identity) tells `Run`'s `finish` helper "the

### 4.6

  store already holds the correct terminal record, don't call

### 4.6

  terminate" apart from every other real failure, which still does.

### 4.6

  The real fix, once a genuine race exposed a check-then-act TOCTOU gap

### 4.6

  between `stopIfCancelled` and the subsequent `Store.Save` (a

### 4.6

  concurrent cancellation landing in that exact window was still

### 4.6

  silently overwritten): `internal/operation/store.go`'s `Save` now

### 4.6

  refuses outright to overwrite an existing terminal record with a

### 4.6

  *different* phase (new `ErrAlreadyTerminal`), enforced under the same

### 4.6

  lock that also now serializes `Save` calls to begin with (`Store`

### 4.6

  gained a `sync.Mutex` — nothing before this task ever called `Save`

### 4.6

  concurrently for the same ID, so the temp-file-plus-rename write

### 4.6

  itself was never actually safe against concurrent writers; task 4.6

### 4.6

  is what first introduces that scenario). This closes the TOCTOU gap

### 4.6

  for every caller, not just ones that remember to check first — the

### 4.6

  cooperative check above is now an optimization (avoid unnecessary

### 4.6

  work), not the sole correctness mechanism. Idempotent re-saves of the

### 4.6

  *same* terminal phase are still allowed.

### 4.6

  Abandoned-partial cleanup (`internal/operation/cleanup.go`, new file):

### 4.6

  `CleanupAbandonedPartials(store, modelsDir) (int, error)` — design.md

### 4.6

  decision 4: "Cancellation removes partials only when explicitly

### 4.6

  requested by policy. A separate cleanup operation handles abandoned

### 4.6

  partials." Removes `.part` files belonging only to `PhaseCancelled`

### 4.6

  operations, deliberately not `PhaseFailed` ones (a failed operation's

### 4.6

  partial can still be genuinely resumable via task 4.3's downloadOne

### 4.6

  if a client resubmits the same plan; a cancelled one was stopped on

### 4.6

  purpose and nothing resubmits it automatically). Same pattern as

### 4.6

  `Store.Prune` (task 2.2): a real, tested, callable function, not

### 4.6

  wired to any periodic scheduler or HTTP endpoint — no task in this

### 4.6

  change adds either, and no OpenAPI route exists for triggering a

### 4.6

  cleanup pass.

### 4.6

  Tests: `internal/operation/store_test.go` — 2 new cases proving

### 4.6

  `ErrAlreadyTerminal` (a stale non-terminal save is rejected after a

### 4.6

  terminal one landed; re-saving the same terminal phase is still

### 4.6

  allowed). `internal/operation/executor_test.go` — 1 new end-to-end

### 4.6

  cancellation test reproducing the real handler's exact shape (a

### 4.6

  second `Load`/`Cancel`/`Save` sequence against the same store,

### 4.6

  concurrently with a running `Run`), using a real `httptest.Server`

### 4.6

  whose handler sends one chunk, sleeps 50ms (long enough that the

### 4.6

  test's own fast cancel sequence always completes first — the actual

### 4.6

  bug this test caught was subtler than simple timing, see below),

### 4.6

  then sends the rest; asserts the final stored phase is `cancelled`,

### 4.6

  not clobbered. `internal/operation/cleanup_test.go`, new file — 3

### 4.6

  tests: only a cancelled operation's partial is removed (a failed

### 4.6

  one's and an in-flight one's are both left alone), a missing partial

### 4.6

  isn't an error, and an empty store isn't an error.

### 4.6

  Debugging note kept here because it's a real methodology point, not

### 4.6

  just a war story: the first version of the cancellation test flaked

### 4.6

  consistently, and t.Logf's buffered output initially made the two

### 4.6

  concurrent goroutines' events look correctly ordered when they

### 4.6

  weren't — switching to fmt.Fprintf(os.Stderr, ...) with explicit

### 4.6

  RFC3339Nano timestamps on both sides was what actually revealed the

### 4.6

  TOCTOU gap in true chronological order. All debug instrumentation was

### 4.6

  removed before landing.

### 4.6

  Verified: `gofmt -l` clean; `GOWORK=off go build ./...`, `go vet

### 4.6

  ./...`, `go test ./... -count=1` green (1349 tests, 31 packages);

### 4.6

  `go test ./... -race -count=1` green across the *entire* repo, not

### 4.6

  just the touched packages, given this task changed real cross-

### 4.6

  goroutine synchronization. `make check-codegen` passes with no diff —

### 4.6

  no OpenAPI schema touched (the cancel endpoint's documented

### 4.6

  "asynchronous" contract already covered this behavior).

### 4.7

  Resubmitting the same install plan (a fresh Operation targeting a

### 4.7

  destination an earlier, already-succeeded operation fully installed)

### 4.7

  is the closest thing to a "retry" this system has — no retry API

### 4.7

  exists in this contract (checked again for this task; still true).

### 4.7

  Two halves closed this out:

### 4.7

  - Registration itself was already idempotent for free:

### 4.7

  `registerInstalledModel` calls `writeModelToConfig`, which upserts

### 4.7

  via `yamlMapSet` rather than erroring on an existing ID — verified

### 4.7

  by reading it, not assumed.

### 4.7

  - The genuinely missing half: `downloadOne` only ever checked a

### 4.7

  `.part` file's size (task 4.3's resume shortcut); it never looked

### 4.7

  at whether the artifact's *final* destination already existed and

### 4.7

  matched. A resubmitted plan for an already-installed model would

### 4.7

  have silently redownloaded the entire artifact from scratch every

### 4.7

  time. Fixed: `downloadOne` now stats the final destination first

### 4.7

  and, if its size already matches the declared total, records that

### 4.7

  size as downloaded and returns immediately — no network call, no

### 4.7

  `.part` file ever created. `verify` and `install` both updated to

### 4.7

  match: `verify` checks the final file directly when no `.part`

### 4.7

  file exists (still applying the same size *and* digest checks — a

### 4.7

  same-size-but-corrupt existing file is not silently accepted just

### 4.7

  because the network was skipped); `install` skips the rename

### 4.7

  entirely for an artifact that's already at its final path.

### 4.7

  Tests: 3 new cases in `internal/operation/executor_test.go` — the

### 4.7

  happy path (server would fail the test if hit at all; asserts no

### 4.7

  `.part` file is ever created and the existing content is left

### 4.7

  untouched), a same-path-wrong-size file correctly triggering a real

### 4.7

  redownload (proving the shortcut isn't over-eager), and a

### 4.7

  same-size-but-wrong-digest existing file still failing verify

### 4.7

  (proving the shortcut doesn't bypass real verification, just the

### 4.7

  network fetch). Caught and fixed a hand-counted-string-length bug in

### 4.7

  my own first draft of the last test via its own defensive

### 4.7

  length-equality assertion, before the test ever ran for real.

### 4.7

  Also fixed the top-of-file `Executor` doc comment, which had drifted:

### 4.7

  it still listed task 4.6 as an unimplemented limitation even though

### 4.7

  4.6 landed in the previous commit — found while updating it for 4.7

### 4.7

  and rewritten to reflect the actual current state of all of section 4.

### 4.7

  Verified: `gofmt -l` clean; `GOWORK=off go build ./...`, `go vet

### 4.7

  ./...`, `go test ./... -count=1` green (1352 tests, 31 packages);

### 4.7

  `go test ./... -race -count=1` green across the entire repo.

### 4.7

  `make check-codegen` passes with no diff — no OpenAPI schema touched.

### 4.7

  Also fixed another accidental duplicate task-list line (same pattern

### 4.7

  as 4.5's), found while updating this entry.

## 5. Inventory and lifecycle

### 5.1

  Scoped deliberately: design.md decision 6 says llama-swap's existing

### 5.1

  process-state vocabulary (Model.state: stopped/starting/ready/

### 5.1

  stopping/shutdown/failed, already shipped pre-this-change) "remains

### 5.1

  the runtime source," to be *enriched*, not replaced. The

### 5.1

  configured/installed/loading distinction and "failure reason and

### 5.1

  freshness" from decision 6's enrichment list were mostly already

### 5.1

  covered (a model appearing in this list at all means configured;

### 5.1

  Model.last_error already reports failure detail; PhaseDownloading

### 5.1

  etc. from section 4's Executor is the "loading" of an install, surfaced

### 5.1

  below via active_operation_id, not a new top-level enum value invented

### 5.1

  for this task). What decision 6 actually asked this task to add:

### 5.1

  "installed-on-disk and configured distinctions," "exact

### 5.1

  source/provenance when known," and "active model operation." That's

### 5.1

  what landed.

### 5.1

  `contracts/llama-skein.openapi.json`: 5 new optional `Model`

### 5.1

  properties — `installed` (bool), `source_repository`/

### 5.1

  `source_revision` (string), `artifact_paths` (string[]),

### 5.1

  `active_operation_id` (string). Regenerated

### 5.1

  `pkg/apicontract/llama_skein.gen.go` (idempotent, verified via two

### 5.1

  successive `go generate` runs producing identical MD5, same

### 5.1

  discipline as every prior contract change in this session).

### 5.1

  New `internal/server/apimodelprovenance.go`: `modelOperationIndex`

### 5.1

  (active/succeeded maps keyed by model_id) + `buildModelOperationIndex`

### 5.1

  (one `operationStore.List()` scan, reused across every model in a

### 5.1

  list response rather than one scan per model) +

### 5.1

  `addProvenanceAndOperationFields`. Provenance (source_repository/

### 5.1

  source_revision/artifact_paths) comes from the most recent

### 5.1

  **succeeded** operation whose `registration.model_id` matches —

### 5.1

  there is no persistent provenance store separate from the operation

### 5.1

  records themselves; a model configured by hand, pulled via the older

### 5.1

  `POST /api/models/pull` route, or whose founding operation record has

### 5.1

  since been pruned (`Store.Prune`, not currently wired to run

### 5.1

  automatically — see task 2.2/4.6 notes) simply has no recoverable

### 5.1

  provenance, and the fields are omitted, not defaulted. This is an

### 5.1

  explicit, documented trade-off: correct today (nothing prunes

### 5.1

  automatically yet) but worth revisiting if/when pruning is wired up.

### 5.1

  `active_operation_id` comes from the most recent **non-terminal**

### 5.1

  operation for that model_id. `installed` stats the primary weights

### 5.1

  path (`parseModelPath(mc.Cmd)`) directly — true/false whenever a path

### 5.1

  is parseable, omitted only when it isn't (matches `addFileMeta`'s

### 5.1

  existing convention).

### 5.1

  Wired into both `handleAPIListModels` and `handleAPIGetModel`

### 5.1

  (`internal/server/apimodels.go`) — the first-ever dedicated test

### 5.1

  coverage for either handler; neither had any before this task.

### 5.1

  New `internal/server/apimodels_test.go`, 7 tests: basic list shape,

### 5.1

  `installed` true/false for present/missing weights files, all four

### 5.1

  new fields omitted when no operation matches, provenance populated

### 5.1

  from a real succeeded operation record, `active_operation_id`

### 5.1

  populated from a real non-terminal one (and provenance fields

### 5.1

  correctly still absent since it hasn't succeeded yet), the same

### 5.1

  provenance fields present on the single-model GET, and a nil

### 5.1

  `operationStore` degrading gracefully rather than breaking the list.

### 5.1

  Explicitly NOT done in this task: `handleAPIListModels`/

### 5.1

  `handleAPIGetModel` still build hand-rolled `map[string]any`

### 5.1

  responses rather than the generated `apicontract.Model` struct

### 5.1

  directly — design.md decision 1's "handwritten DTOs... removed after

### 5.1

  migration" goal is section 6's (Client migration) charter, not this

### 5.1

  one's; changing the response-building mechanism here risked breaking

### 5.1

  existing fields (`details`, `unlisted`, GGUF metadata) this task

### 5.1

  never touched. `artifact_paths` reports what the founding operation

### 5.1

  *submitted*, not a live re-scan of what's actually present at the

### 5.1

  destination now — a file manually deleted after install won't be

### 5.1

  reflected until `installed` (checked only against the primary

### 5.1

  weights path) or a future operation notices.

### 5.1

  Verified: `gofmt -l` clean; `GOWORK=off go build ./...`, `go vet

### 5.1

  ./...`, `go test ./... -count=1` green (1359 tests, 31 packages);

### 5.1

  `go test ./internal/server/... -race -count=1` green (320 tests).

### 5.1

  `go generate ./pkg/apicontract` run twice produced an identical MD5

### 5.1

  (idempotent) — `make check-codegen` itself shows a diff pre-commit

### 5.1

  by design (it compares against git HEAD); re-checked clean after

### 5.1

  committing, per this session's established discipline.

### 5.2

  Routing through upstream lifecycle was already true (`handleAPILoadModel`

### 5.2

  warms via the local router's own `ServeHTTP`; `handleAPIUnloadModel`/

### 5.2

  `handleAPIUnloadAll` call the router's `Unload`, which blocks until each

### 5.2

  targeted process has actually exited per its own doc comment) — this

### 5.2

  task's real work was the second half, "expose observable terminal

### 5.2

  outcomes," which was genuinely missing in two places:

### 5.2

  1. **`handleAPILoadModel` always reported success.** It already

### 5.2

  captured the warm request's status (`dw.status`) but never checked

### 5.2

  it — real bug, not a documentation gap: the endpoint returned

### 5.2

  `200 "OK"` even when the warm request failed outright. Fixed:

### 5.2

  after warming, checks both `dw.status >= 400` and the resulting

### 5.2

  `state == "failed"` (they can disagree — a request-level timeout

### 5.2

  vs. a still-starting process, or a 200 for a process that then

### 5.2

  crashes from something unrelated to that specific warm call) and

### 5.2

  returns `502` with `model`/`state`/`loaded`/`load_request_status`/

### 5.2

  `last_error` on either. The success response is deliberately

### 5.2

  **unchanged** — still plain `"OK"` text at `200` — since nothing

### 5.2

  could have depended on a specific failure shape that never existed

### 5.2

  before, but changing the success shape would be a needless

### 5.2

  breaking change.

### 5.2

  2. **`handleAPIUnloadModel`/`handleAPIUnloadAll` had no way to

### 5.2

  surface an anomaly.** `Unload` has no return value at all

### 5.2

  (`func Unload(timeout, models...)`) and its contract already

### 5.2

  guarantees the caller stays blocked until the process has exited,

### 5.2

  so this is a defensive confirmation, not a routine code path:

### 5.2

  after `Unload` returns, re-checks the model's state and, only if

### 5.2

  it's still reported loaded (a genuine violation of `Unload`'s own

### 5.2

  contract), returns `500` instead of the previous unconditional

### 5.2

  `200 "OK"`. `handleAPIUnloadAll` reports the same anomaly via a

### 5.2

  new, purely additive `still_running` array rather than changing

### 5.2

  its status code — the existing `{"msg":"ok"}` shape is preserved

### 5.2

  exactly when nothing is wrong.

### 5.2

  3. **Corrects a mistaken claim in 5.1's own completion note above**:

### 5.2

  it said `Model.last_error` "already reports failure detail" —

### 5.2

  checked again for this task and that was wrong. The field has

### 5.2

  been documented on the `Model` schema since before this change,

### 5.2

  but neither `handleAPIListModels` nor `handleAPIGetModel` ever

### 5.2

  actually populated it; only the unrelated health endpoint

### 5.2

  (`api.go`) did, via the same underlying `s.local.ModelErrors()`

### 5.2

  this task now also wires into both inventory handlers (built once

### 5.2

  per handler call, same reasoning as 5.1's `opIdx`).

### 5.2

  Extended `internal/server/server_test.go`'s shared `stubRouter` with

### 5.2

  a `serveStatus` field (zero means the pre-existing 200 default) so

### 5.2

  tests can simulate a failed warm request — the one existing struct

### 5.2

  literal construction site (`newStubRouter`) is field-named, so this

### 5.2

  is a purely additive, non-breaking extension of shared test

### 5.2

  infrastructure.

### 5.2

  Tests: 5 new in `internal/server/apimodels_test.go` (`last_error`

### 5.2

  populated on both list and get; the success response shape provably

### 5.2

  unchanged; a failed warm request reported as 502; a 200 warm request

### 5.2

  whose process still ends up "failed" also reported as 502, with the

### 5.2

  real crash detail attached) and 4 new in

### 5.2

  `internal/server/apigroup_test.go` (the unload anomaly path on both

### 5.2

  single-model and bulk unload; the additive `still_running` field

### 5.2

  present only when non-empty, absent — not empty — on the normal

### 5.2

  path). All of extras_test.go's three pre-existing unload tests still

### 5.2

  pass unmodified, confirming the ordinary success path is untouched.

### 5.2

  Verified: `gofmt -l` clean; `GOWORK=off go build ./...`, `go vet

### 5.2

  ./...`, `go test ./... -count=1` green (1367 tests, 31 packages);

### 5.2

  `go test ./internal/server/... -race -count=1` green (328 tests).

### 5.2

  `make check-codegen` passes with no diff — no OpenAPI schema touched

### 5.2

  by this task (last_error already existed on Model; the load/unload

### 5.2

  response changes are additive JSON fields on endpoints that were

### 5.2

  never contract-typed in the first place, same hand-rolled-map

### 5.2

  situation task 5.1 already documented and deliberately left alone).

### 5.3

  Found the same category of gap 4.6 and 5.2 each found in this

### 5.3

  change: `handleAPIDeleteModel` (`DELETE /api/models/{model}`)

### 5.3

  pre-existed this change but did none of design.md decision 6's four

### 5.3

  things except the first:

### 5.3

  - **unloads affected models**: already correct, kept as-is.

### 5.3

  - **validates artifact ownership**: did not exist at all — the

### 5.3

  primary weights path was read straight from the model's `cmd` and

### 5.3

  passed to `os.Remove` with zero containment checking. A

### 5.3

  hand-edited or corrupted config entry could have pointed anywhere

### 5.3

  on the host and this endpoint would have deleted it. Fixed: new

### 5.3

  `internal/server/apimodelremoval.go`'s `pathIsContained` (exact-

### 5.3

  directory-prefix match against `s.modelsDir()`, the same

### 5.3

  discipline `operation.ResolveArtifactDestination` established in

### 5.3

  task 3.3 for the "models" vs "models-archive" false-positive) —

### 5.3

  checked before anything is touched; the whole request is refused

### 5.3

  (422) if `modelsDir` is unknown or any candidate path fails

### 5.3

  containment, never a partial, unvalidated delete.

### 5.3

  - **removes the full artifact set**: previously deleted only the

### 5.3

  single primary weights file. New `resolveArtifactSetForRemoval`

### 5.3

  resolves the complete set two ways: (1) from the most recent

### 5.3

  succeeded install operation for this model_id (task 5.1's

### 5.3

  provenance) — every artifact path it submitted, resolved under its

### 5.3

  own `source_repository`, the accurate and complete answer

### 5.3

  (weights, shards, auxiliaries) for anything installed through the

### 5.3

  operation API; (2) a shard-sibling scan of the primary file's own

### 5.3

  directory (`operation.GroupShards`, task 3.2/4.5's convention) as

### 5.3

  the fallback for a model with no operation provenance (configured

### 5.3

  by hand, or pulled via the older `POST /api/models/pull` route).

### 5.3

  - **removes config in one explicit operation**: previously never

### 5.3

  touched config at all — a deleted model stayed configured, pointing

### 5.3

  at a now-missing file. Fixed: reuses `removeModelFromConfig`, the

### 5.3

  same function `DELETE /api/config/models/{id}` already uses,

### 5.3

  rather than duplicating it; best-effort when `configFile` isn't

### 5.3

  set (the artifact files are already gone by that point, so that

### 5.3

  real progress is reported, not discarded behind an error).

### 5.3

  Response shape: kept the pre-existing `"deleted"` key (backward

### 5.3

  compatible — same meaning it always had, the primary weights path)

### 5.3

  and added `deleted_files`/`missing_files`/`config_removed` alongside

### 5.3

  it, additive. A shard set where most files are already gone but one

### 5.3

  remains still counts as real removal work (200, not 404) — only

### 5.3

  "every candidate path was already missing" is treated as nothing to

### 5.3

  delete.

### 5.3

  Tests: new `internal/server/apimodelremoval_test.go`, 7 cases — the

### 5.3

  happy path with no provenance (unload-then-delete-then-config-remove,

### 5.3

  backward-compat key present); a path outside `modelsDir` refused

### 5.3

  (422, file survives); `modelsDir` unknown refused; shard-sibling

### 5.3

  fallback deleting both shards with no operation record; the full set

### 5.3

  (weights + tokenizer) from real operation provenance; a partially-

### 5.3

  missing set still succeeding; and `configFile` unset still deleting

### 5.3

  files while reporting `config_removed: false`.

### 5.3

  Verified: `gofmt -l` clean; `GOWORK=off go build ./...`, `go vet

### 5.3

  ./...`, `go test ./... -count=1` green (1374 tests, 31 packages);

### 5.3

  `go test ./internal/server/... -race -count=1` green (335 tests).

### 5.3

  `make check-codegen` passes with no diff — this endpoint was already

### 5.3

  hand-rolled before this change (same deferred-DTO-migration situation

### 5.3

  5.1/5.2 documented), so no OpenAPI schema was touched.

### 5.4

  Two real, concrete gaps found by auditing every config-writing path

### 5.4

  in `internal/server` against the patterns `patchModelInConfig`

### 5.4

  already established (its own doc comment: "Snapshot the canonical

### 5.4

  form before mutation so a no-op patch... is detected by content"):

### 5.4

  1. **No-op detection didn't exist for whole-model writes/removals at

### 5.4

  all** — only `patchModelInConfig` had it; `writeModelToConfig`

### 5.4

  (used by config-API add, task 4.1's install registration, and the

### 5.4

  older pull route) and `removeModelFromConfig` (used by config-API

### 5.4

  remove and task 5.3's delete) always wrote and always reloaded

### 5.4

  unconditionally, even when the resulting content was

### 5.4

  byte-identical to what was already on disk. Fixed: both gained

### 5.4

  the exact same before/after-marshal comparison

### 5.4

  `patchModelInConfig` uses, now returning `(changed bool, err

### 5.4

  error)`; every one of their 4 callers updated to skip

### 5.4

  `SetPending`+`triggerReload` when `changed` is false. This isn't

### 5.4

  hypothetical for the install path specifically — it's task 4.7's

### 5.4

  idempotent-reinstall scenario made concrete: resubmitting the

### 5.4

  same plan for an already-succeeded, already-registered model now

### 5.4

  genuinely skips the reload (and the model restart it would

### 5.4

  cause) instead of triggering one for a config file that didn't

### 5.4

  change.

### 5.4

  2. **`registerInstalledModel` (task 4.1) never called `SetPending`

### 5.4

  at all** — every other config-writing handler in this package

### 5.4

  stages a real actor/summary before `triggerReload()` so

### 5.4

  `internal/config.SnapshotConfig` (invoked generically at the

### 5.4

  reload boundary in `llama-skein.go`) attributes the resulting

### 5.4

  history snapshot correctly; this one silently fell through to the

### 5.4

  generic `"reload"` default (`RuntimeState.TakePending`'s own doc

### 5.4

  comment) for every model ever installed through the operation

### 5.4

  API. Fixed: now calls

### 5.4

  `SetPending("api:install-model", "installed model <id>

### 5.4

  (operation <op.ID>)")` before reloading.

### 5.4

  `handleAPIConfigAddModel` (config-API add) also picked up the

### 5.4

  no-op skip as a side effect of `writeModelToConfig`'s signature

### 5.4

  change, even though it wasn't this task's original target — audited

### 5.4

  while updating every caller for the new signature, so it seemed

### 5.4

  wrong to leave it inconsistent with the others.

### 5.4

  Model-ID collision detection at install time (validateInstallPlan

### 5.4

  accepting a plan for an already-configured model_id, silently

### 5.4

  overwriting it) remains explicitly out of scope — task 3.3's own

### 5.4

  completion note already deferred it as needing "a real config to

### 5.4

  check against, which this layer doesn't hold," and no task in this

### 5.4

  list reopens it; this task is about reusing existing safety

### 5.4

  mechanisms consistently, not adding new validation.

### 5.4

  Tests: new `internal/server/apiconfignoop_test.go`, 7 cases —

### 5.4

  `writeModelToConfig`/`removeModelFromConfig` each reporting

### 5.4

  `changed` correctly on a real write vs. an identical rewrite (with

### 5.4

  the config file's actual bytes verified unchanged, not just the

### 5.4

  return value trusted), `registerInstalledModel` staging a real

### 5.4

  actor/summary (not the `"reload"` default) on first registration and

### 5.4

  correctly staging nothing on an identical second one, and the same

### 5.4

  no-op behavior confirmed through the real HTTP route

### 5.4

  (`POST /api/config/models`) via the config file's content rather

### 5.4

  than a reload call count — `triggerReload` runs `reloadFn` in its

### 5.4

  own goroutine, and asserting a negative ("no reload happened") by

### 5.4

  racing that goroutine would repeat the exact flakiness task 4.6

### 5.4

  already hit and fixed once this session.

### 5.4

  Verified: `gofmt -l` clean; `GOWORK=off go build ./...`, `go vet

### 5.4

  ./...`, `go test ./... -count=1` green (1381 tests, 31 packages);

### 5.4

  `go test ./internal/server/... -race -count=1` green (342 tests).

### 5.4

  `make check-codegen` passes with no diff — no OpenAPI schema touched

### 5.4

  by this task.
