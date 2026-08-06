package server

import (
	"os"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/operation"
)

// modelOperationIndex maps a model_id to the two operation records GET
// /api/models (task 5.1, design.md decision 6: "active model operation";
// "exact source/provenance when known") needs to report on: the most
// recent still-running one, if any, and the most recent succeeded one (the
// best available provenance source). Built by scanning the whole operation
// store once — store.List() is already a full scan of every record
// regardless of how many models ask, so building this once per handler
// call and reusing it per-model is strictly better than one List() call
// per model.
type modelOperationIndex struct {
	active    map[string]*operation.Operation
	succeeded map[string]*operation.Operation
}

// buildModelOperationIndex scans s.operationStore once. An unavailable
// store (nil, or a List error) yields an empty index — every model then
// simply reports no provenance and no active operation, the same as if it
// were configured by hand or through the older pull route; this is a
// degraded-but-safe fallback, not an error surfaced to the caller.
func (s *Server) buildModelOperationIndex() modelOperationIndex {
	idx := modelOperationIndex{active: map[string]*operation.Operation{}, succeeded: map[string]*operation.Operation{}}
	if s.operationStore == nil {
		return idx
	}
	ops, err := s.operationStore.List() // newest first
	if err != nil {
		return idx
	}
	for _, op := range ops {
		id := op.Registration.ModelID
		if id == "" {
			continue
		}
		if !op.Phase.Terminal() {
			if _, exists := idx.active[id]; !exists {
				idx.active[id] = op
			}
			continue
		}
		if op.Phase == operation.PhaseSucceeded {
			if _, exists := idx.succeeded[id]; !exists {
				// List() returns newest-created first, so the first
				// succeeded match for this ID is the most recent install.
				idx.succeeded[id] = op
			}
		}
	}
	return idx
}

// addProvenanceAndOperationFields adds task 5.1's new fields to entry:
// "installed" (always set when the model's weights path is known),
// "source_repository"/"source_revision"/"artifact_paths" (only when a
// succeeded founding operation is still on record), and
// "active_operation_id" (only when a non-terminal one exists). Every field
// here is best-effort and omitted rather than defaulted when unknown — a
// model configured by hand, pulled via the older POST /api/models/pull
// route, or whose founding operation record has since been pruned simply
// has no recoverable provenance, which is a legitimate, expected state,
// not an error.
func addProvenanceAndOperationFields(entry map[string]any, id string, mc config.ModelConfig, idx modelOperationIndex) {
	if p := parseModelPath(mc.Cmd); p != "" {
		if _, err := os.Stat(p); err == nil {
			entry["installed"] = true
		} else {
			entry["installed"] = false
		}
	}
	if op, ok := idx.succeeded[id]; ok {
		entry["source_repository"] = op.SourceRepository
		entry["source_revision"] = op.SourceRevision
		paths := make([]string, len(op.Artifacts))
		for i, a := range op.Artifacts {
			paths[i] = a.Path
		}
		entry["artifact_paths"] = paths
	}
	if op, ok := idx.active[id]; ok {
		entry["active_operation_id"] = op.ID
	}
}
