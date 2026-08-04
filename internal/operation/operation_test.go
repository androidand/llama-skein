package operation

import (
	"strings"
	"testing"
)

func TestNew_GeneratesPrefixedID(t *testing.T) {
	op := New("org/model", "deadbeef", "model-id", nil, testNow)
	if !strings.HasPrefix(op.ID, "op_") {
		t.Fatalf("ID = %q, want an op_-prefixed ID", op.ID)
	}
	if op.Phase != PhaseQueued {
		t.Fatalf("Phase = %s, want queued", op.Phase)
	}
	if !op.CreatedAt.Equal(testNow) || !op.UpdatedAt.Equal(testNow) {
		t.Fatalf("CreatedAt/UpdatedAt = %s/%s, want both %s", op.CreatedAt, op.UpdatedAt, testNow)
	}
}

func TestNew_GeneratesDistinctIDs(t *testing.T) {
	a := New("org/model", "rev", "id", nil, testNow)
	b := New("org/model", "rev", "id", nil, testNow)
	if a.ID == b.ID {
		t.Fatalf("two operations share ID %q", a.ID)
	}
}

func TestOperation_BytesDownloadedSumsArtifacts(t *testing.T) {
	op := &Operation{Artifacts: []ArtifactProgress{
		{Path: "a", BytesDownloaded: 100},
		{Path: "b", BytesDownloaded: 250},
	}}
	if got := op.BytesDownloaded(); got != 350 {
		t.Fatalf("BytesDownloaded() = %d, want 350", got)
	}
}

func TestOperation_BytesTotalNilWhenAnyArtifactUnknown(t *testing.T) {
	known := int64(100)
	op := &Operation{Artifacts: []ArtifactProgress{
		{Path: "a", BytesTotal: &known},
		{Path: "b", BytesTotal: nil},
	}}
	if got := op.BytesTotal(); got != nil {
		t.Fatalf("BytesTotal() = %v, want nil (one artifact's total is unknown)", got)
	}
}

func TestOperation_BytesTotalSumsWhenAllKnown(t *testing.T) {
	a, b := int64(100), int64(250)
	op := &Operation{Artifacts: []ArtifactProgress{
		{Path: "a", BytesTotal: &a},
		{Path: "b", BytesTotal: &b},
	}}
	got := op.BytesTotal()
	if got == nil || *got != 350 {
		t.Fatalf("BytesTotal() = %v, want 350", got)
	}
}

func TestPhase_Terminal(t *testing.T) {
	for _, phase := range []Phase{PhaseSucceeded, PhaseCancelled, PhaseFailed} {
		if !phase.Terminal() {
			t.Errorf("%s.Terminal() = false, want true", phase)
		}
	}
	for _, phase := range []Phase{PhaseQueued, PhasePreflighting, PhaseDownloading} {
		if phase.Terminal() {
			t.Errorf("%s.Terminal() = true, want false", phase)
		}
	}
}

func TestPhase_Valid(t *testing.T) {
	for _, phase := range happyPath {
		if !phase.Valid() {
			t.Errorf("%s.Valid() = false, want true", phase)
		}
	}
	if !PhaseCancelled.Valid() || !PhaseFailed.Valid() {
		t.Fatal("cancelled/failed should be valid phases")
	}
	if Phase("bogus").Valid() {
		t.Fatal(`Phase("bogus").Valid() = true, want false`)
	}
}
