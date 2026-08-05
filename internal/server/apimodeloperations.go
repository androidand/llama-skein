package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/operation"
	"github.com/androidand/llama-skein/pkg/apicontract"
)

// eventStreamPollInterval is how often handleAPIStreamModelOperationEvents
// re-checks the store for a changed snapshot. Polling, not a pub/sub
// mechanism, is the deliberate first-slice choice — the store has no
// subscriber list yet and every operation is also reachable via GET, so a
// missed or delayed event is never data loss, only a slightly stale stream.
const eventStreamPollInterval = 500 * time.Millisecond

// maxTerminalOperations bounds retained succeeded/cancelled/failed operation
// records; see operation.Store.Prune. Not yet wired to a periodic prune call
// (nothing schedules one in this task) — that lands with a later task once
// there is real download/install traffic to observe pruning against.
const maxTerminalOperations = 50

// defaultDiskSafetyReserveBytes is the headroom validateInstallPlan leaves
// on top of the plan's own artifact bytes (design.md decision 4). Not
// user-configurable yet — there is no config field for it and no task in
// this change asks for one; 5 GiB is a placeholder sized to comfortably
// cover a llama.cpp build's own working files (KV cache spill, logs) on a
// host disk, not a value derived from measurement. A later task can promote
// this to a config field if a fixed constant proves wrong in practice.
const defaultDiskSafetyReserveBytes = 5 << 30

func newOperationStore() (*operation.Store, error) {
	dir, err := operation.DefaultStateDir()
	if err != nil {
		return nil, err
	}
	return operation.NewStore(dir, maxTerminalOperations)
}

// operationsUnavailable reports and responds when the store failed to
// initialize (newOperationStore's error was logged, not fatal, at startup).
func (s *Server) operationsUnavailable(w http.ResponseWriter) bool {
	if s.operationStore != nil {
		return false
	}
	writeOperationError(w, http.StatusServiceUnavailable, "model operations are unavailable: could not initialize the operation state directory")
	return true
}

// handleAPICreateModelOperation implements POST /api/models/operations:
// submit an install plan, create the operation (design.md decision 2).
func (s *Server) handleAPICreateModelOperation(w http.ResponseWriter, r *http.Request) {
	if s.operationsUnavailable(w) {
		return
	}
	var plan apicontract.ModelInstallPlan
	if err := json.NewDecoder(r.Body).Decode(&plan); err != nil {
		writeOperationError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if reason := validateInstallPlan(plan, s.modelsDir()); reason != "" {
		writeOperationError(w, http.StatusBadRequest, reason)
		return
	}
	// plan.Token is a request secret (design.md decision 7): read only as far
	// as this line, then discarded. operation.Operation has no field capable
	// of holding it, so it cannot reach the persisted record, a log line, or
	// an error message by construction — not by remembering not to include
	// it. Still not handed to anything: task 4.1's executor authenticates no
	// requests yet (gated-repository downloads need it; ungated ones don't),
	// so there is still nothing to hand it to.
	plan.Token = nil

	op := operation.NewFromPlan(toOperationPlan(plan), time.Now())
	if err := s.operationStore.Save(op); err != nil {
		writeOperationError(w, http.StatusInternalServerError, "could not persist operation: "+err.Error())
		return
	}
	resp := toAPIModelOperation(op)
	writeJSONStatus(w, http.StatusCreated, resp)

	// Execution runs on s.shutdownCtx, not r.Context(): task 4.2's "retain
	// deterministic partial files independently of client connection
	// lifetime" requires the download to keep going after this handler (and
	// the request that reached it) returns, and it does — shutdownCtx is
	// cancelled only when the server process itself shuts down. Dispatched
	// strictly after resp is built from op's fields above: from this line on
	// the goroutine owns op exclusively. s.runOperation is nil in every
	// test-constructed Server (see its doc comment on the Server struct),
	// so ordinary handler tests never make a real network call here.
	if s.runOperation != nil {
		go s.runOperation(s.shutdownCtx, op)
	}
}

// toOperationPlan converts the wire ModelInstallPlan to operation.Plan, the
// subset execution (task 4.1's Executor) needs, captured once at accept time
// so it survives independently of the request (design.md decision 2: the
// plan is immutable once accepted).
func toOperationPlan(plan apicontract.ModelInstallPlan) operation.Plan {
	artifacts := make([]operation.Artifact, len(plan.Artifacts))
	for i, a := range plan.Artifacts {
		artifacts[i] = operation.Artifact{
			Path:      a.Path,
			SizeBytes: a.SizeBytes,
			Digest:    a.Digest,
			Role:      operation.ArtifactRole(a.Role),
		}
	}
	reg := operation.Registration{
		ModelID:     plan.Registration.ModelId,
		DisplayName: plan.Registration.DisplayName,
		Backend:     string(plan.Registration.Backend),
		TTL:         plan.Registration.Ttl,
	}
	if plan.Registration.Flags != nil {
		reg.Flags = *plan.Registration.Flags
	}
	return operation.Plan{
		SourceRepository: plan.SourceRepository,
		SourceRevision:   plan.SourceRevision,
		Artifacts:        artifacts,
		Registration:     reg,
	}
}

// newOperationExecutor builds the operation.Executor this server runs every
// create-accepted operation through, wiring its Registrar to
// registerInstalledModel (config write + reload) and its disk-preflight
// reserve to the same defaultDiskSafetyReserveBytes validateInstallPlan
// already checked against once at accept time.
func (s *Server) newOperationExecutor() *operation.Executor {
	return &operation.Executor{
		Store:              s.operationStore,
		ModelsDir:          s.modelsDir(),
		Client:             s.operationHTTPClient,
		SafetyReserveBytes: defaultDiskSafetyReserveBytes,
		Register:           s.registerInstalledModel,
		Logf:               s.proxylog.Infof,
	}
}

// registerInstalledModel is the operation.Registrar this server runs at the
// end of a successful install: write the operation's captured Registration
// snapshot to config, the same writeModelToConfig path
// registerPulledModel (apipull.go) uses for the old handwritten pull
// endpoint, then trigger a reload. Folds design.md decision 3's distinct
// "registering" and "reloading" phases into one call — see
// operation.Executor's doc comment for why that's fine.
func (s *Server) registerInstalledModel(op *operation.Operation, weightsPath string) error {
	if weightsPath == "" {
		return fmt.Errorf("operation %s: no weights artifact resolved to register", op.ID)
	}
	if s.configFile == "" {
		return fmt.Errorf("config file path not set; cannot auto-register")
	}
	flags := ""
	if len(op.Registration.Flags) > 0 {
		flags = strings.Join(op.Registration.Flags, " ")
	}
	mc := config.ModelConfig{
		Cmd:         s.buildCmd(weightsPath, flags),
		Proxy:       "http://127.0.0.1:${PORT}",
		Backend:     op.Registration.Backend,
		UnloadAfter: config.MODEL_CONFIG_DEFAULT_TTL,
	}
	if op.Registration.DisplayName != nil {
		mc.Name = *op.Registration.DisplayName
	}
	if op.Registration.TTL != nil {
		mc.UnloadAfter = *op.Registration.TTL
	}
	if err := s.writeModelToConfig(op.Registration.ModelID, &mc); err != nil {
		return err
	}
	s.triggerReload()
	return nil
}

// digestRe matches the one digest form InstallArtifact.digest documents:
// "sha256:" followed by exactly 64 lowercase hex characters.
var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// validateInstallPlan reports the first reason plan is unacceptable, or ""
// if it may proceed (design.md decision 7 and task 3.3's full list —
// immutable revision, artifact paths, destination containment, required
// roles, size, optional digest). Already-configured model_id collision
// still lands with the actual execution path in a later task: it needs a
// real config to check against, which this layer doesn't hold — only
// modelsDir is threaded through, for the destination-containment check.
func validateInstallPlan(plan apicontract.ModelInstallPlan, modelsDir string) string {
	if plan.SourceRepository == "" {
		return "source_repository is required"
	}
	if plan.SourceRevision == "" {
		return "source_revision is required"
	}
	if len(plan.Artifacts) == 0 {
		return "at least one artifact is required"
	}
	hasWeights := false
	var totalBytes int64
	for _, artifact := range plan.Artifacts {
		if artifact.Path == "" {
			return "every artifact needs a path"
		}
		if artifact.SizeBytes <= 0 {
			return "every artifact needs a positive size_bytes"
		}
		totalBytes += artifact.SizeBytes
		if artifact.Digest != nil && !digestRe.MatchString(*artifact.Digest) {
			return fmt.Sprintf("artifact %s: digest must be \"sha256:\" followed by 64 hex characters", artifact.Path)
		}
		if _, err := operation.ResolveArtifactURL(plan.SourceRepository, plan.SourceRevision, artifact.Path); err != nil {
			return err.Error()
		}
		if modelsDir != "" {
			if _, err := operation.ResolveArtifactDestination(modelsDir, plan.SourceRepository, artifact.Path); err != nil {
				return err.Error()
			}
		}
		if artifact.Role == apicontract.ArtifactRoleWeights {
			hasWeights = true
		}
	}
	if !hasWeights {
		return "at least one artifact must have role \"weights\""
	}
	// Disk preflight needs a real directory to statfs; skipped when modelsDir
	// is unknown, same as the destination-containment check above (task 3.3).
	// remainingBytes is the plan's full artifact total — this is the initial
	// create path, not a resume, so "remaining" and "total" coincide here
	// (see CheckDiskPreflight's doc comment for the distinction that matters
	// once resume, task 4.x, exists).
	if modelsDir != "" {
		if err := operation.CheckDiskPreflight(modelsDir, totalBytes, defaultDiskSafetyReserveBytes); err != nil {
			return err.Error()
		}
	}
	if plan.Registration.ModelId == "" {
		return "registration.model_id is required"
	}
	if reason := validateWeightShardCompleteness(plan.Artifacts); reason != "" {
		return reason
	}
	return ""
}

// validateWeightShardCompleteness rejects a plan whose weights artifacts
// reference part of a shard set without the rest of it (design.md decision
// 5: "llama-skein validates that shard numbering is complete... before
// registration"). Non-weights artifacts (projector/tokenizer/config/other)
// are never sharded in practice and are not grouped or checked.
func validateWeightShardCompleteness(artifacts []apicontract.InstallArtifact) string {
	paths := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Role == apicontract.ArtifactRoleWeights {
			paths = append(paths, artifact.Path)
		}
	}
	for _, group := range operation.GroupShards(paths) {
		if _, ok := operation.ParseShardInfo(group[0]); !ok {
			continue // a singleton non-shard weights file; nothing to check.
		}
		if complete, total := operation.ShardSetComplete(group); !complete {
			return fmt.Sprintf("incomplete shard set: %d of %d parts present (%s)", len(group), total, strings.Join(group, ", "))
		}
	}
	return ""
}

// handleAPIListModelOperations implements GET /api/models/operations:
// bounded history, most recent first.
func (s *Server) handleAPIListModelOperations(w http.ResponseWriter, r *http.Request) {
	if s.operationsUnavailable(w) {
		return
	}
	ops, err := s.operationStore.List()
	if err != nil {
		writeOperationError(w, http.StatusInternalServerError, "could not list operations: "+err.Error())
		return
	}
	resp := apicontract.ModelOperationList{Operations: make([]apicontract.ModelOperation, len(ops))}
	for i, op := range ops {
		resp.Operations[i] = toAPIModelOperation(op)
	}
	writeJSON(w, resp)
}

// handleAPIGetModelOperation implements GET /api/models/operations/{id}.
func (s *Server) handleAPIGetModelOperation(w http.ResponseWriter, r *http.Request) {
	op, err := s.loadOperationOr404(w, r)
	if err != nil {
		return
	}
	writeJSON(w, toAPIModelOperation(op))
}

// handleAPICancelModelOperation implements POST
// /api/models/operations/{id}/cancel. Idempotent per the contract: cancelling
// an already-cancelled operation returns its current snapshot, not an error.
func (s *Server) handleAPICancelModelOperation(w http.ResponseWriter, r *http.Request) {
	op, err := s.loadOperationOr404(w, r)
	if err != nil {
		return
	}
	if cancelErr := op.Cancel(time.Now()); cancelErr != nil {
		// Only reachable when the operation already reached a different
		// terminal phase (succeeded/failed) — cancelling THAT is refused, and
		// that refusal is a real 409, not a 404 or a silent no-op.
		writeOperationError(w, http.StatusConflict, cancelErr.Error())
		return
	}
	if err := s.operationStore.Save(op); err != nil {
		writeOperationError(w, http.StatusInternalServerError, "could not persist cancellation: "+err.Error())
		return
	}
	writeJSONStatus(w, http.StatusAccepted, toAPIModelOperation(op))
}

// handleAPIStreamModelOperationEvents implements GET
// /api/models/operations/{id}/events: SSE, each event's data is one
// ModelOperation snapshot. Supplementary to GET, not a replacement — see the
// route's own description in contracts/llama-skein.openapi.json.
func (s *Server) handleAPIStreamModelOperationEvents(w http.ResponseWriter, r *http.Request) {
	op, err := s.loadOperationOr404(w, r)
	if err != nil {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOperationError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	writeEvent(w, op)
	flusher.Flush()
	if op.Phase.Terminal() {
		return
	}

	ticker := time.NewTicker(eventStreamPollInterval)
	defer ticker.Stop()
	lastUpdatedAt := op.UpdatedAt
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			current, err := s.operationStore.Load(op.ID)
			if err != nil {
				return // the operation record is gone; nothing left to stream.
			}
			if current.UpdatedAt.Equal(lastUpdatedAt) {
				continue
			}
			lastUpdatedAt = current.UpdatedAt
			writeEvent(w, current)
			flusher.Flush()
			if current.Phase.Terminal() {
				return
			}
		}
	}
}

func writeEvent(w http.ResponseWriter, op *operation.Operation) {
	data, err := json.Marshal(toAPIModelOperation(op))
	if err != nil {
		return // an unencodable snapshot is a bug, not a reason to break the stream's framing.
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
}

// errOperationsUnavailable signals that loadOperationOr404 already wrote the
// response (503) and the caller should just return.
var errOperationsUnavailable = errors.New("operation store unavailable")

// loadOperationOr404 loads the operation named by the "id" path value,
// writing a 404 and returning a non-nil error if it does not exist.
func (s *Server) loadOperationOr404(w http.ResponseWriter, r *http.Request) (*operation.Operation, error) {
	if s.operationsUnavailable(w) {
		return nil, errOperationsUnavailable
	}
	id := r.PathValue("id")
	op, err := s.operationStore.Load(id)
	if errors.Is(err, operation.ErrNotFound) {
		writeOperationError(w, http.StatusNotFound, "unknown operation id: "+id)
		return nil, err
	}
	if err != nil {
		writeOperationError(w, http.StatusInternalServerError, "could not load operation: "+err.Error())
		return nil, err
	}
	return op, nil
}

func writeOperationError(w http.ResponseWriter, status int, message string) {
	writeJSONStatus(w, status, apicontract.ErrorResponse{Error: message})
}

// toAPIModelOperation converts the internal domain type to its wire shape.
// Kept as an explicit conversion (not shared fields/embedding) so the
// internal Operation type can evolve without silently changing the contract.
func toAPIModelOperation(op *operation.Operation) apicontract.ModelOperation {
	artifacts := make([]apicontract.ModelOperationArtifactProgress, len(op.Artifacts))
	for i, artifact := range op.Artifacts {
		artifacts[i] = apicontract.ModelOperationArtifactProgress{
			Path:            artifact.Path,
			BytesDownloaded: artifact.BytesDownloaded,
			BytesTotal:      artifact.BytesTotal,
		}
	}
	resp := apicontract.ModelOperation{
		Id:              op.ID,
		Phase:           apicontract.ModelOperationPhase(op.Phase),
		Artifacts:       artifacts,
		BytesDownloaded: op.BytesDownloaded(),
		BytesTotal:      op.BytesTotal(),
		CreatedAt:       op.CreatedAt,
		UpdatedAt:       op.UpdatedAt,
	}
	if op.ModelID != "" && op.Phase == operation.PhaseSucceeded {
		resp.ModelId = ptrOf(op.ModelID)
	}
	if op.Error != nil {
		resp.Error = &apicontract.ModelOperationError{
			Code:    apicontract.ModelOperationErrorCode(op.Error.Code),
			Message: op.Error.Message,
		}
	}
	if len(op.Warnings) > 0 {
		resp.Warnings = &op.Warnings
	}
	return resp
}
