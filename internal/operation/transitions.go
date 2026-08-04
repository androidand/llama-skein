package operation

import (
	"errors"
	"fmt"
	"time"
)

// ErrInvalidTransition is returned by Operation.TransitionTo for a phase
// change the state machine does not allow.
var ErrInvalidTransition = errors.New("operation: invalid phase transition")

// happyPath is the linear, non-terminal-branching sequence design.md decision
// 3 defines. Every phase here except the last may also transition to
// PhaseCancelled or PhaseFailed instead of advancing.
var happyPath = []Phase{
	PhaseQueued,
	PhasePreflighting,
	PhaseResolving,
	PhaseDownloading,
	PhaseVerifying,
	PhaseInstalling,
	PhaseRegistering,
	PhaseReloading,
	PhaseSucceeded,
}

var happyPathIndex = func() map[Phase]int {
	index := make(map[Phase]int, len(happyPath))
	for i, phase := range happyPath {
		index[phase] = i
	}
	return index
}()

// CanTransition reports whether an operation may move from `from` to `to`.
// Terminal phases (succeeded, cancelled, failed) never transition further.
// Any non-terminal phase may transition to cancelled or failed. Otherwise
// the only legal move is one step forward along the happy path — phases
// cannot be skipped or reordered.
func CanTransition(from, to Phase) bool {
	if from.Terminal() {
		return false
	}
	if to == PhaseCancelled || to == PhaseFailed {
		return true
	}
	fromIndex, fromOK := happyPathIndex[from]
	toIndex, toOK := happyPathIndex[to]
	if !fromOK || !toOK {
		return false
	}
	return toIndex == fromIndex+1
}

// TransitionTo validates and applies a phase change, updating UpdatedAt on
// success. It does not itself decide *when* a transition should happen —
// callers (2.2's persistence, 2.3's handlers) drive the sequence; this only
// refuses illegal moves.
func (o *Operation) TransitionTo(to Phase, now time.Time) error {
	if !CanTransition(o.Phase, to) {
		return fmt.Errorf("%w: %s -> %s (operation %s)", ErrInvalidTransition, o.Phase, to, o.ID)
	}
	o.Phase = to
	o.UpdatedAt = now
	return nil
}

// Fail transitions the operation to PhaseFailed and records a typed error.
// A no-op error (redundant success-path recording) is still recorded — the
// terminal Error field always reflects the actual failure that ended the
// operation, never a stale one from an earlier retry.
func (o *Operation) Fail(code ErrorCode, message string, now time.Time) error {
	if err := o.TransitionTo(PhaseFailed, now); err != nil {
		return err
	}
	o.Error = &Error{Code: code, Message: message}
	return nil
}

// Cancel transitions the operation to PhaseCancelled. Idempotent: cancelling
// an operation that is already cancelled is a no-op, matching the API's
// documented idempotent-cancel behavior (contracts/llama-skein.openapi.json,
// POST /api/models/operations/{id}/cancel). Cancelling an operation that has
// already reached a different terminal phase (succeeded or failed) is
// refused — those outcomes are not overridable after the fact.
func (o *Operation) Cancel(now time.Time) error {
	if o.Phase == PhaseCancelled {
		return nil
	}
	return o.TransitionTo(PhaseCancelled, now)
}
