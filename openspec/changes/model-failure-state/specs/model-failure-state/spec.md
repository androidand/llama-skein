# Model Failure State and Caller-Visible Readiness

## ADDED Requirements

### Requirement: Failure is a distinct state with a retained error
The process state model SHALL include a `failed` state distinct from `stopped`. On a start or load failure the system SHALL retain a `last_error` carrying at minimum a message, a category, a timestamp, and the attempt count, and SHALL expose it everywhere `state` is exposed. A model that failed to load SHALL NOT report the same state as a model that was never asked to load.

#### Scenario: Load fails
- **WHEN** a model's backend fails to start or fails its readiness check
- **THEN** the model reports state `failed` with a populated `last_error`, rather than reporting `stopped`

#### Scenario: Idle model never requested
- **WHEN** a configured model has never been requested
- **THEN** it reports state `stopped` with no `last_error`, distinguishable from a failed model

#### Scenario: Recovery after a failure
- **WHEN** a previously failed model subsequently loads successfully
- **THEN** it reports `ready`, and the prior `last_error` is retained as history rather than presented as the current condition

### Requirement: Readiness before response commitment
The system SHALL NOT commit an HTTP status or response body for an inference request until the target model's load has either succeeded or failed. When the load fails, the caller SHALL receive an error status code and a typed error body. An error SHALL NOT be delivered inside an already-committed success stream.

#### Scenario: Load fails while loading state is enabled
- **WHEN** `sendLoadingState` is enabled and a streaming request targets a model whose load then fails
- **THEN** the caller receives an error status code with a typed error body, and does not receive HTTP 200 with the error embedded in the stream

#### Scenario: Load succeeds
- **WHEN** the load succeeds
- **THEN** the response commits with 200 and streams normally, and loading progress may be streamed once success is assured

#### Scenario: Signature that must no longer occur
- **WHEN** any inference request completes
- **THEN** the combination of status 200, a multi-minute duration, and zero valid JSON deltas SHALL NOT occur; that outcome is reported as an error status instead

### Requirement: Pollable provider readiness
`GET /health` SHALL return a body reporting, per configured model, its `state` and `last_error`, and at provider level whether any model is resident and whether the provider is currently busy. A caller SHALL be able to determine from this single endpoint whether dispatching an inference request is likely to succeed.

#### Scenario: Preflight before dispatch
- **WHEN** an orchestrator polls `/health` before dispatching
- **THEN** it can distinguish ready, loading, busy, failed, and idle-with-no-model-resident without issuing an inference request

#### Scenario: Reachable but not serving
- **WHEN** the control plane is reachable and no model is resident
- **THEN** `/health` reports no resident model, rather than a bare `200 OK` implying health

### Requirement: Readiness is in the API contract
`contracts/llama-skein.openapi.json` SHALL describe `/running`, `/api/models`, and the `/health` response body. `Model.state` SHALL include `failed`, and a `LastError` schema SHALL be defined. The contract SHALL be edited before the implementation.

#### Scenario: Cross-repo client generation
- **WHEN** a consumer generates a client from the contract
- **THEN** the generated types expose per-model `state` including `failed`, and `last_error`

### Requirement: Documented fail-fast codes
The system SHALL document its failure status codes as a contract, and load failure SHALL use a code distinct from the generic server-error catch-all, so that a caller can distinguish "this model will not load" from an unspecified internal error.

#### Scenario: Model will not load
- **WHEN** a request targets a model whose load fails
- **THEN** the response carries the documented load-failure code, not the generic catch-all

#### Scenario: Caller distinguishes failure classes
- **WHEN** a caller receives a failure response
- **THEN** the documented table lets it distinguish insufficient-capacity, queue-timeout, concurrency-rejected, and load-failure without parsing prose

### Requirement: Host-level session cap
The system SHALL enforce an explicit host-level limit on concurrent inference sessions, defaulting to 1 for single-slot backends. A request exceeding the cap SHALL be rejected promptly with the documented concurrency code rather than queued invisibly.

#### Scenario: Second concurrent session on a single-slot host
- **WHEN** a second inference request arrives while one is in flight on a single-slot backend
- **THEN** it is rejected promptly with the documented code, rather than silently queueing behind the first

#### Scenario: Cap is not the HTTP middleware default
- **WHEN** the host-level cap is in effect
- **THEN** it governs concurrent inference sessions independently of the general HTTP concurrency middleware limit, which does not enforce the one-session invariant

## MODIFIED Requirements

### Requirement: Health endpoint reports substance
`GET /health` SHALL NOT be a hardcoded constant. It SHALL reflect actual model and provider condition.

#### Scenario: Health under a wedged model
- **WHEN** a model is wedged or failed
- **THEN** `/health` reflects that condition rather than returning an unconditional success
