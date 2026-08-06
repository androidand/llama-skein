package operation

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T, maxTerminal int) *Store {
	t.Helper()
	store, err := NewStore(t.TempDir(), maxTerminal)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func TestStore_SaveThenLoadRoundTrips(t *testing.T) {
	store := newTestStore(t, 10)
	total := int64(1024)
	op := New("org/model", "deadbeef", "model-id", []ArtifactProgress{
		{Path: "model.gguf", BytesDownloaded: 512, BytesTotal: &total},
	}, testNow)
	op.Warnings = []string{"digest unverified"}

	if err := store.Save(op); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load(op.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ID != op.ID || got.Phase != op.Phase || got.SourceRepository != op.SourceRepository ||
		got.SourceRevision != op.SourceRevision || got.ModelID != op.ModelID {
		t.Fatalf("Load() = %+v, want match for %+v", got, op)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[0].BytesDownloaded != 512 || *got.Artifacts[0].BytesTotal != 1024 {
		t.Fatalf("Artifacts = %+v, want the saved artifact progress", got.Artifacts)
	}
	if len(got.Warnings) != 1 || got.Warnings[0] != "digest unverified" {
		t.Fatalf("Warnings = %v, want [\"digest unverified\"]", got.Warnings)
	}
}

func TestStore_LoadUnknownIDReturnsErrNotFound(t *testing.T) {
	store := newTestStore(t, 10)
	_, err := store.Load("op_doesnotexist")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestStore_SaveIsAtomic(t *testing.T) {
	store := newTestStore(t, 10)
	op := New("org/model", "deadbeef", "model-id", nil, testNow)
	if err := store.Save(op); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// No leftover .tmp file after a successful save.
	entries, err := os.ReadDir(store.dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".tmp" {
			t.Fatalf("leftover temp file: %s", entry.Name())
		}
	}
}

func TestStore_SaveOverwritesPreviousState(t *testing.T) {
	store := newTestStore(t, 10)
	op := New("org/model", "deadbeef", "model-id", nil, testNow)
	if err := store.Save(op); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if err := op.TransitionTo(PhasePreflighting, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("TransitionTo: %v", err)
	}
	if err := store.Save(op); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	got, err := store.Load(op.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Phase != PhasePreflighting {
		t.Fatalf("Phase = %s, want preflighting (the second save should win)", got.Phase)
	}
}

// TestStore_SaveRejectsRegressingATerminalRecord is task 4.6's TOCTOU
// safety net (see Save's ErrAlreadyTerminal doc comment): once a record
// reaches a terminal phase, a save carrying a different phase must be
// rejected outright, not silently accepted as "the second save wins" the
// way TestStore_SaveOverwritesPreviousState expects for ordinary
// non-terminal progress.
func TestStore_SaveRejectsRegressingATerminalRecord(t *testing.T) {
	store := newTestStore(t, 10)
	op := New("org/model", "deadbeef", "model-id", nil, testNow)
	if err := op.Cancel(testNow); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := store.Save(op); err != nil {
		t.Fatalf("first Save (cancelled): %v", err)
	}

	// A second, stale in-memory copy that never learned about the
	// cancellation — exactly Executor.Run's shape: its own op object is
	// still "downloading" (or whatever phase it was in) when it tries to
	// save progress after a concurrent /cancel request already finalized
	// the record.
	stale := New("org/model", "deadbeef", "model-id", nil, testNow)
	stale.ID = op.ID
	if err := stale.TransitionTo(PhasePreflighting, testNow); err != nil {
		t.Fatalf("TransitionTo: %v", err)
	}

	err := store.Save(stale)
	if !errors.Is(err, ErrAlreadyTerminal) {
		t.Fatalf("Save(stale) error = %v, want ErrAlreadyTerminal", err)
	}

	got, loadErr := store.Load(op.ID)
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	if got.Phase != PhaseCancelled {
		t.Fatalf("Phase = %s, want cancelled — the rejected save must not have taken effect", got.Phase)
	}
}

// TestStore_SaveAllowsReaffirmingTheSameTerminalPhase proves the guard
// only blocks a *different* terminal phase, not an idempotent re-save of
// the same one (e.g. two /cancel requests racing, or a caller re-persisting
// the same terminal record for an unrelated field update).
func TestStore_SaveAllowsReaffirmingTheSameTerminalPhase(t *testing.T) {
	store := newTestStore(t, 10)
	op := New("org/model", "deadbeef", "model-id", nil, testNow)
	if err := op.Cancel(testNow); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := store.Save(op); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	op.Warnings = append(op.Warnings, "an additional note")
	if err := store.Save(op); err != nil {
		t.Fatalf("second Save (same terminal phase) should be allowed: %v", err)
	}
	got, err := store.Load(op.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want the second save's update to have taken effect", got.Warnings)
	}
}

func TestStore_ListReturnsNewestFirst(t *testing.T) {
	store := newTestStore(t, 10)
	older := New("org/a", "rev", "a", nil, testNow)
	newer := New("org/b", "rev", "b", nil, testNow.Add(time.Hour))
	if err := store.Save(older); err != nil {
		t.Fatalf("Save older: %v", err)
	}
	if err := store.Save(newer); err != nil {
		t.Fatalf("Save newer: %v", err)
	}
	ops, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ops) != 2 || ops[0].ID != newer.ID || ops[1].ID != older.ID {
		t.Fatalf("List() = %v, want [newer, older]", idsOf(ops))
	}
}

func TestStore_ListSkipsCorruptFilesRatherThanFailing(t *testing.T) {
	store := newTestStore(t, 10)
	good := New("org/a", "rev", "a", nil, testNow)
	if err := store.Save(good); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.dir, "op_corrupt.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	ops, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ops) != 1 || ops[0].ID != good.ID {
		t.Fatalf("List() = %v, want only the good operation", idsOf(ops))
	}
}

func TestStore_PruneKeepsAllNonTerminalOperations(t *testing.T) {
	store := newTestStore(t, 0) // zero terminal slots retained
	inFlight := New("org/a", "rev", "a", nil, testNow)
	if err := store.Save(inFlight); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, err := store.Load(inFlight.ID); err != nil {
		t.Fatalf("in-flight operation was pruned: %v", err)
	}
}

func TestStore_PruneRemovesOldestTerminalOperationsBeyondTheBound(t *testing.T) {
	store := newTestStore(t, 2)
	var ids []string
	for i := range 4 {
		op := New("org/a", "rev", "a", nil, testNow.Add(time.Duration(i)*time.Minute))
		if err := op.TransitionTo(PhaseFailed, testNow); err != nil {
			t.Fatalf("TransitionTo: %v", err)
		}
		if err := store.Save(op); err != nil {
			t.Fatalf("Save: %v", err)
		}
		ids = append(ids, op.ID)
	}
	if err := store.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	ops, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("List() after prune has %d operations, want 2", len(ops))
	}
	// The two most recently created (last in the loop) must survive.
	kept := map[string]bool{ops[0].ID: true, ops[1].ID: true}
	if !kept[ids[2]] || !kept[ids[3]] {
		t.Fatalf("kept %v, want the two newest (%s, %s)", idsOf(ops), ids[2], ids[3])
	}
}

func TestStore_PruneDoesNotRemoveTerminalOperationsWithinTheBound(t *testing.T) {
	store := newTestStore(t, 5)
	op := New("org/a", "rev", "a", nil, testNow)
	if err := op.Cancel(testNow); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := store.Save(op); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, err := store.Load(op.ID); err != nil {
		t.Fatalf("operation within the bound was pruned: %v", err)
	}
}

func TestDefaultStateDir_EndsUnderDotLlamaSkein(t *testing.T) {
	dir, err := DefaultStateDir()
	if err != nil {
		t.Fatalf("DefaultStateDir: %v", err)
	}
	want := filepath.Join(".llama-skein", "operations")
	if filepath.Base(filepath.Dir(dir)) != ".llama-skein" || filepath.Base(dir) != "operations" {
		t.Fatalf("DefaultStateDir() = %q, want it to end in %q", dir, want)
	}
}

func idsOf(ops []*Operation) []string {
	ids := make([]string, len(ops))
	for i, op := range ops {
		ids[i] = op.ID
	}
	return ids
}
