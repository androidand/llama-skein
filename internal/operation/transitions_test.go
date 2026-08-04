package operation

import (
	"errors"
	"testing"
	"time"
)

var testNow = time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)

func TestCanTransition_HappyPathAdvancesOneStepAtATime(t *testing.T) {
	for i := 0; i < len(happyPath)-1; i++ {
		from, to := happyPath[i], happyPath[i+1]
		if !CanTransition(from, to) {
			t.Errorf("CanTransition(%s, %s) = false, want true", from, to)
		}
	}
}

func TestCanTransition_RefusesSkippingPhases(t *testing.T) {
	if CanTransition(PhaseQueued, PhaseDownloading) {
		t.Fatal("CanTransition(queued, downloading) = true, want false — skips preflighting/resolving")
	}
	if CanTransition(PhaseQueued, PhaseVerifying) {
		t.Fatal("CanTransition(queued, verifying) = true, want false")
	}
}

func TestCanTransition_RefusesGoingBackward(t *testing.T) {
	if CanTransition(PhaseDownloading, PhaseResolving) {
		t.Fatal("CanTransition(downloading, resolving) = true, want false")
	}
	if CanTransition(PhaseVerifying, PhaseQueued) {
		t.Fatal("CanTransition(verifying, queued) = true, want false")
	}
}

func TestCanTransition_AnyNonTerminalPhaseCanFailOrCancel(t *testing.T) {
	for _, phase := range happyPath {
		if phase.Terminal() {
			continue
		}
		if !CanTransition(phase, PhaseFailed) {
			t.Errorf("CanTransition(%s, failed) = false, want true", phase)
		}
		if !CanTransition(phase, PhaseCancelled) {
			t.Errorf("CanTransition(%s, cancelled) = false, want true", phase)
		}
	}
}

func TestCanTransition_TerminalPhasesNeverTransition(t *testing.T) {
	terminal := []Phase{PhaseSucceeded, PhaseCancelled, PhaseFailed}
	destinations := append(append([]Phase{}, happyPath...), PhaseCancelled, PhaseFailed)
	for _, from := range terminal {
		for _, to := range destinations {
			if CanTransition(from, to) {
				t.Errorf("CanTransition(%s, %s) = true, want false — %s is terminal", from, to, from)
			}
		}
	}
}

func TestCanTransition_RejectsUnknownPhases(t *testing.T) {
	if CanTransition(PhaseQueued, Phase("bogus")) {
		t.Fatal("CanTransition(queued, bogus) = true, want false")
	}
	if CanTransition(Phase("bogus"), PhaseQueued) {
		t.Fatal("CanTransition(bogus, queued) = true, want false")
	}
}

func TestOperation_TransitionToAppliesAndStampsUpdatedAt(t *testing.T) {
	op := New("org/model", "deadbeef", "model-id", nil, testNow)
	later := testNow.Add(time.Minute)

	if err := op.TransitionTo(PhasePreflighting, later); err != nil {
		t.Fatalf("TransitionTo: %v", err)
	}
	if op.Phase != PhasePreflighting {
		t.Fatalf("Phase = %s, want preflighting", op.Phase)
	}
	if !op.UpdatedAt.Equal(later) {
		t.Fatalf("UpdatedAt = %s, want %s", op.UpdatedAt, later)
	}
	if !op.CreatedAt.Equal(testNow) {
		t.Fatalf("CreatedAt changed: %s, want %s", op.CreatedAt, testNow)
	}
}

func TestOperation_TransitionToRejectsInvalidMoveAndLeavesStateUnchanged(t *testing.T) {
	op := New("org/model", "deadbeef", "model-id", nil, testNow)
	later := testNow.Add(time.Minute)

	err := op.TransitionTo(PhaseDownloading, later)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v, want ErrInvalidTransition", err)
	}
	if op.Phase != PhaseQueued {
		t.Fatalf("Phase = %s, want unchanged queued", op.Phase)
	}
	if !op.UpdatedAt.Equal(testNow) {
		t.Fatalf("UpdatedAt changed on a rejected transition: %s, want unchanged %s", op.UpdatedAt, testNow)
	}
}

func TestOperation_FailRecordsTypedErrorAndTransitions(t *testing.T) {
	op := New("org/model", "deadbeef", "model-id", nil, testNow)
	later := testNow.Add(time.Minute)

	if err := op.Fail(ErrorDigestMismatch, "expected sha256:aaa, got sha256:bbb", later); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if op.Phase != PhaseFailed {
		t.Fatalf("Phase = %s, want failed", op.Phase)
	}
	if op.Error == nil || op.Error.Code != ErrorDigestMismatch {
		t.Fatalf("Error = %+v, want code digest_mismatch", op.Error)
	}
}

func TestOperation_FailFromTerminalPhaseIsRejected(t *testing.T) {
	op := New("org/model", "deadbeef", "model-id", nil, testNow)
	if err := op.Cancel(testNow); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := op.Fail(ErrorInternal, "too late", testNow.Add(time.Minute)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Fail after cancel: err = %v, want ErrInvalidTransition", err)
	}
}

func TestOperation_CancelIsIdempotent(t *testing.T) {
	op := New("org/model", "deadbeef", "model-id", nil, testNow)
	first := testNow.Add(time.Minute)
	if err := op.Cancel(first); err != nil {
		t.Fatalf("first Cancel: %v", err)
	}
	if !op.UpdatedAt.Equal(first) {
		t.Fatalf("UpdatedAt after first cancel = %s, want %s", op.UpdatedAt, first)
	}

	second := first.Add(time.Minute)
	if err := op.Cancel(second); err != nil {
		t.Fatalf("second Cancel: %v", err)
	}
	if op.Phase != PhaseCancelled {
		t.Fatalf("Phase = %s, want cancelled", op.Phase)
	}
	// A no-op cancel must not disturb the timestamp of the real transition.
	if !op.UpdatedAt.Equal(first) {
		t.Fatalf("UpdatedAt after idempotent cancel = %s, want unchanged %s", op.UpdatedAt, first)
	}
}

func TestOperation_CancelAfterSucceededIsRejected(t *testing.T) {
	op := New("org/model", "deadbeef", "model-id", nil, testNow)
	for i := 0; i < len(happyPath)-1; i++ {
		if err := op.TransitionTo(happyPath[i+1], testNow); err != nil {
			t.Fatalf("advancing to %s: %v", happyPath[i+1], err)
		}
	}
	if op.Phase != PhaseSucceeded {
		t.Fatalf("setup: Phase = %s, want succeeded", op.Phase)
	}
	if err := op.Cancel(testNow.Add(time.Minute)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Cancel after succeeded: err = %v, want ErrInvalidTransition", err)
	}
}
