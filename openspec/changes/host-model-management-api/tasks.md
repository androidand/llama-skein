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
- [ ] 1.3 Define capability, artifact role, install plan, model operation,
  progress, outcome, and typed error schemas in OpenAPI.
- [ ] 1.4 Add generated lifecycle and operation client methods; regenerate Go
  clients and validate the opencode-skein TypeScript generation path.

## 2. Host operation domain

- [ ] 2.1 Implement the explicit model-operation state machine and validated
  phase transitions.
- [ ] 2.2 Implement bounded atomic operation-record persistence in the owned
  llama-skein state directory.
- [ ] 2.3 Add operation create/get/list/event-stream/cancel handlers through
  generated contract types.
- [ ] 2.4 Recover interrupted nonterminal operations at startup and expose
  resumable partial-artifact information.
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
