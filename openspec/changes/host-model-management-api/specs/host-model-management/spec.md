## ADDED Requirements

### Requirement: Host model management is contract-first
llama-skein SHALL define inventory, model detail, installation, operation
observation, cancellation, load, unload, and removal in the OpenAPI source of
truth and SHALL generate cross-repository clients from that contract.

#### Scenario: Client is regenerated
- **WHEN** the model-management contract changes
- **THEN** Go and TypeScript clients are regenerated without handwritten duplicate wire schemas

### Requirement: Install identity is immutable and complete
An installation request MUST identify a Hugging Face repository, immutable
revision, and complete required artifact set with paths, sizes, roles, and
available digests.

#### Scenario: Mutable or incomplete identity is submitted
- **WHEN** a request omits an immutable revision or required shard
- **THEN** llama-skein rejects the plan before downloading bytes

### Requirement: Model mutations use observable operations
llama-skein SHALL return a stable operation ID for long-running model
mutations and SHALL expose phase, progress, timestamps, warnings, outcome, and
typed failure information independently of the initiating connection.

#### Scenario: Client reconnects during download
- **WHEN** the initiating client disconnects and later requests the operation ID
- **THEN** llama-skein reports the current or terminal operation state without restarting the operation

#### Scenario: Host restarts during download
- **WHEN** llama-skein starts with a previously nonterminal operation record
- **THEN** it marks the prior attempt interrupted and exposes resumable partial artifact state

### Requirement: Downloads are resumable and atomic
llama-skein SHALL retain valid partial artifacts, use HTTP ranges when safely
supported, verify final size and available digest, and atomically rename
verified artifacts into the models directory.

#### Scenario: Download is interrupted
- **WHEN** a transfer ends before the declared artifact size
- **THEN** the final destination remains absent and a subsequent operation can resume from the valid partial bytes

#### Scenario: Verification fails
- **WHEN** final size or digest differs from the installation plan
- **THEN** llama-skein fails the operation and does not register or expose the artifact as installed

### Requirement: Multi-file models install as one unit
llama-skein SHALL validate and install complete GGUF shard sets and required
auxiliary files as one installation plan.

#### Scenario: One shard fails
- **WHEN** any required shard or auxiliary artifact fails download or verification
- **THEN** the model is not registered and the operation identifies the failed artifact

### Requirement: Host safety is checked before mutation
llama-skein MUST verify destination containment, source policy, remaining disk
capacity, loaded state, and affected artifact ownership before changing model
files or configuration.

#### Scenario: Free space is insufficient
- **WHEN** remaining disk plus configured reserve cannot hold the outstanding artifacts
- **THEN** llama-skein rejects the operation during preflight without starting the transfer

#### Scenario: Removal targets an unmanaged path
- **WHEN** a removal request resolves outside the configured models directory or does not match the model's recorded artifact set
- **THEN** llama-skein refuses the destructive operation

### Requirement: Model state distinguishes lifecycle concerns
Inventory SHALL distinguish configured, installed, loading, ready, failed,
and unloaded state and SHALL include active operation and failure details when
present.

#### Scenario: Configured model file is absent
- **WHEN** a model exists in configuration but its required artifact is missing
- **THEN** inventory reports configured but not installed rather than unloaded and usable

### Requirement: Cancellation is explicit and idempotent
Clients SHALL be able to cancel a cancellable model operation by ID without
depending on transport disconnection.

#### Scenario: Cancellation is repeated
- **WHEN** cancellation is requested more than once for the same operation
- **THEN** llama-skein returns the same terminal cancellation state without additional side effects

### Requirement: Model management works without Skein
All host model-management capabilities SHALL be usable directly by
opencode-skein without a running Skein or llmfit process.

#### Scenario: Skein is offline
- **WHEN** opencode-skein discovers and connects to llama-skein while Skein is unavailable
- **THEN** inventory, fit, installation, progress, load, unload, and removal remain available
