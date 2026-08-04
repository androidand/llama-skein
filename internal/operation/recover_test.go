package operation

import (
	"testing"
	"time"
)

// advanceTo steps op through the happy path up to and including target,
// exercising the real transition rules rather than jumping straight there.
func advanceTo(op *Operation, target Phase, now time.Time) error {
	for _, phase := range happyPath {
		if phase == PhaseQueued {
			continue // op already starts here
		}
		if err := op.TransitionTo(phase, now); err != nil {
			return err
		}
		if phase == target {
			return nil
		}
	}
	return nil
}

func TestRecover_MarksNonTerminalOperationsInterrupted(t *testing.T) {
	store := newTestStore(t, 10)
	op := New("org/model", "deadbeef", "model-id", nil, testNow)
	if err := advanceTo(op, PhaseDownloading, testNow); err != nil {
		t.Fatalf("advanceTo: %v", err)
	}
	if err := store.Save(op); err != nil {
		t.Fatalf("Save: %v", err)
	}

	recovered, err := Recover(store, testNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(recovered) != 1 || recovered[0].ID != op.ID {
		t.Fatalf("Recover() = %v, want [%s]", idsOf(recovered), op.ID)
	}
	if len(recovered[0].Warnings) != 1 || recovered[0].Warnings[0] != "interrupted by a server restart while at phase downloading" {
		t.Fatalf("Warnings = %v, want one interruption warning naming the phase", recovered[0].Warnings)
	}
	// The phase itself must be unchanged — recovery marks, it does not transition.
	if recovered[0].Phase != PhaseDownloading {
		t.Fatalf("Phase = %s, want unchanged downloading", recovered[0].Phase)
	}

	// Persisted, not just returned.
	reloaded, err := store.Load(op.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reloaded.Warnings) != 1 {
		t.Fatalf("persisted Warnings = %v, want the interruption warning saved", reloaded.Warnings)
	}
}

func TestRecover_LeavesTerminalOperationsAlone(t *testing.T) {
	store := newTestStore(t, 10)
	op := New("org/model", "deadbeef", "model-id", nil, testNow)
	if err := op.Cancel(testNow); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := store.Save(op); err != nil {
		t.Fatalf("Save: %v", err)
	}

	recovered, err := Recover(store, testNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(recovered) != 0 {
		t.Fatalf("Recover() = %v, want no terminal operations recovered", idsOf(recovered))
	}
	reloaded, err := store.Load(op.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reloaded.Warnings) != 0 {
		t.Fatalf("Warnings = %v, want untouched terminal operation to have none", reloaded.Warnings)
	}
}

func TestRecover_IsIdempotentAcrossRepeatedRestarts(t *testing.T) {
	store := newTestStore(t, 10)
	op := New("org/model", "deadbeef", "model-id", nil, testNow)
	if err := store.Save(op); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := Recover(store, testNow.Add(time.Hour)); err != nil {
		t.Fatalf("first Recover: %v", err)
	}
	if _, err := Recover(store, testNow.Add(2*time.Hour)); err != nil {
		t.Fatalf("second Recover: %v", err)
	}

	reloaded, err := store.Load(op.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reloaded.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want exactly one interruption warning after two Recover calls", reloaded.Warnings)
	}
}

func TestRecover_PreservesLastPersistedArtifactProgress(t *testing.T) {
	store := newTestStore(t, 10)
	total := int64(1000)
	op := New("org/model", "deadbeef", "model-id", []ArtifactProgress{
		{Path: "model.gguf", BytesDownloaded: 400, BytesTotal: &total},
	}, testNow)
	if err := advanceTo(op, PhaseDownloading, testNow); err != nil {
		t.Fatalf("advanceTo: %v", err)
	}
	if err := store.Save(op); err != nil {
		t.Fatalf("Save: %v", err)
	}

	recovered, err := Recover(store, testNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(recovered) != 1 || recovered[0].BytesDownloaded() != 400 {
		t.Fatalf("recovered progress = %+v, want BytesDownloaded 400 preserved", recovered)
	}
}

func TestRecover_ReturnsEmptyForAnEmptyStore(t *testing.T) {
	store := newTestStore(t, 10)
	recovered, err := Recover(store, testNow)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(recovered) != 0 {
		t.Fatalf("Recover() = %v, want none", idsOf(recovered))
	}
}
