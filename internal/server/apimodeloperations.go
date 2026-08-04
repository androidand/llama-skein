package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

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
	if reason := validateInstallPlan(plan); reason != "" {
		writeOperationError(w, http.StatusBadRequest, reason)
		return
	}
	// plan.Token is a request secret (design.md decision 7): read only as far
	// as this line, then discarded. operation.Operation has no field capable
	// of holding it, so it cannot reach the persisted record, a log line, or
	// an error message by construction — not by remembering not to include
	// it. Not yet handed to anything: no download execution exists yet
	// (sections 3-4) to authenticate with it.
	plan.Token = nil

	artifacts := make([]operation.ArtifactProgress, len(plan.Artifacts))
	for i, artifact := range plan.Artifacts {
		total := artifact.SizeBytes
		artifacts[i] = operation.ArtifactProgress{Path: artifact.Path, BytesTotal: &total}
	}

	op := operation.New(plan.SourceRepository, plan.SourceRevision, plan.Registration.ModelId, artifacts, time.Now())
	if err := s.operationStore.Save(op); err != nil {
		writeOperationError(w, http.StatusInternalServerError, "could not persist operation: "+err.Error())
		return
	}
	writeJSONStatus(w, http.StatusCreated, toAPIModelOperation(op))
}

// validateInstallPlan reports the first reason plan is unacceptable, or ""
// if it may proceed. Only structural/request-shape validation belongs here —
// trust-boundary checks (design.md decision 7: HTTPS-only Hugging Face hosts,
// destination containment, already-configured model_id) land with the actual
// execution path in a later task, once there is somewhere for them to run
// before download starts rather than only at submission time.
func validateInstallPlan(plan apicontract.ModelInstallPlan) string {
	if plan.SourceRepository == "" {
		return "source_repository is required"
	}
	if plan.SourceRevision == "" {
		return "source_revision is required"
	}
	if len(plan.Artifacts) == 0 {
		return "at least one artifact is required"
	}
	for _, artifact := range plan.Artifacts {
		if artifact.Path == "" {
			return "every artifact needs a path"
		}
		if artifact.SizeBytes <= 0 {
			return "every artifact needs a positive size_bytes"
		}
	}
	if plan.Registration.ModelId == "" {
		return "registration.model_id is required"
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
