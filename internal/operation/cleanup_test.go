package operation

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupAbandonedPartials_RemovesOnlyCancelledOperationsPartials(t *testing.T) {
	modelsDir := t.TempDir()
	store := testExecutorStore(t)
	now := time.Now()

	cancelled := NewFromPlan(Plan{
		SourceRepository: "org/cancelled-repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []Artifact{{Path: "model.gguf", SizeBytes: 100, Role: ArtifactRoleWeights}},
		Registration:     Registration{ModelID: "cancelled-model"},
	}, now)
	if err := cancelled.Cancel(now); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := store.Save(cancelled); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cancelledPartial := filepath.Join(modelsDir, "org", "cancelled-repo", "model.gguf.part")
	seedFile(t, cancelledPartial, []byte("abandoned partial bytes"))

	failed := NewFromPlan(Plan{
		SourceRepository: "org/failed-repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []Artifact{{Path: "model.gguf", SizeBytes: 100, Role: ArtifactRoleWeights}},
		Registration:     Registration{ModelID: "failed-model"},
	}, now)
	if err := failed.Fail(ErrorInternal, "some transient error", now); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if err := store.Save(failed); err != nil {
		t.Fatalf("Save: %v", err)
	}
	failedPartial := filepath.Join(modelsDir, "org", "failed-repo", "model.gguf.part")
	seedFile(t, failedPartial, []byte("possibly-resumable partial bytes"))

	inFlight := NewFromPlan(Plan{
		SourceRepository: "org/in-flight-repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []Artifact{{Path: "model.gguf", SizeBytes: 100, Role: ArtifactRoleWeights}},
		Registration:     Registration{ModelID: "in-flight-model"},
	}, now)
	// Only PhaseCancelled/PhaseFailed/PhaseSucceeded are reachable directly
	// from any phase; any other forward move is one step at a time
	// (transitions.go's happyPath) — PhasePreflighting is enough here since
	// this test only needs "any non-terminal phase," not a specific one.
	if err := inFlight.TransitionTo(PhasePreflighting, now); err != nil {
		t.Fatalf("TransitionTo: %v", err)
	}
	if err := store.Save(inFlight); err != nil {
		t.Fatalf("Save: %v", err)
	}
	inFlightPartial := filepath.Join(modelsDir, "org", "in-flight-repo", "model.gguf.part")
	seedFile(t, inFlightPartial, []byte("actively downloading"))

	removed, err := CleanupAbandonedPartials(store, modelsDir)
	if err != nil {
		t.Fatalf("CleanupAbandonedPartials: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (only the cancelled operation's partial)", removed)
	}

	if _, err := os.Stat(cancelledPartial); !os.IsNotExist(err) {
		t.Fatalf("cancelled operation's partial should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(failedPartial); err != nil {
		t.Fatalf("failed operation's partial should be retained (still possibly resumable): %v", err)
	}
	if _, err := os.Stat(inFlightPartial); err != nil {
		t.Fatalf("in-flight operation's partial should never be touched: %v", err)
	}
}

func TestCleanupAbandonedPartials_MissingPartialIsNotAnError(t *testing.T) {
	modelsDir := t.TempDir()
	store := testExecutorStore(t)
	now := time.Now()

	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []Artifact{{Path: "model.gguf", SizeBytes: 100, Role: ArtifactRoleWeights}},
		Registration:     Registration{ModelID: "my-model"},
	}, now)
	if err := op.Cancel(now); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := store.Save(op); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Deliberately no partial file seeded — the cancellation happened
	// before any bytes were ever written.

	removed, err := CleanupAbandonedPartials(store, modelsDir)
	if err != nil {
		t.Fatalf("CleanupAbandonedPartials: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
}

func TestCleanupAbandonedPartials_EmptyStore(t *testing.T) {
	store := testExecutorStore(t)
	removed, err := CleanupAbandonedPartials(store, t.TempDir())
	if err != nil {
		t.Fatalf("CleanupAbandonedPartials: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
}

func seedFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
