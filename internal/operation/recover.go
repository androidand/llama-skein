package operation

import "time"

// interruptedWarningPrefix marks a warning added by Recover, so a later
// Recover call on the same still-non-terminal operation (e.g. the process
// crashed again before anything resumed it) does not append a duplicate.
const interruptedWarningPrefix = "interrupted by a server restart while at phase "

// Recover finds every non-terminal operation in store — by definition, if a
// non-terminal record exists at startup, no live process is working on it
// (the process that was is the one that just restarted) — and marks each one
// interrupted. It returns every recovered operation so the caller can log or
// surface them.
//
// The phase itself is left unchanged: design.md's phase vocabulary has no
// separate "interrupted" state, because phase describes which step an
// operation reached, not whether a process is currently advancing it. A
// warning captures the distinction instead.
//
// Each operation's Artifacts (with whatever BytesDownloaded was last
// persisted) is the "resumable partial-artifact information" this task
// exposes. It does not reconcile against actual bytes on disk — no
// deterministic partial-file naming/location scheme exists yet (design.md
// decision 4, "operation/artifact-specific suffix"), because nothing
// downloads artifacts yet (sections 3-4). Reconciling against a naming
// scheme that doesn't exist would be guessing, not recovery.
func Recover(store *Store, now time.Time) ([]*Operation, error) {
	ops, err := store.List()
	if err != nil {
		return nil, err
	}
	recovered := make([]*Operation, 0, len(ops))
	for _, op := range ops {
		if op.Phase.Terminal() {
			continue
		}
		if op.markInterrupted(now) {
			if err := store.Save(op); err != nil {
				return recovered, err
			}
		}
		recovered = append(recovered, op)
	}
	return recovered, nil
}

// markInterrupted appends an interruption warning naming the current phase,
// unless the most recent warning already records the same interruption.
// Returns whether it changed anything (so the caller knows whether a Save is
// needed).
func (o *Operation) markInterrupted(now time.Time) bool {
	message := interruptedWarningPrefix + string(o.Phase)
	if len(o.Warnings) > 0 && o.Warnings[len(o.Warnings)-1] == message {
		return false
	}
	o.Warnings = append(o.Warnings, message)
	o.UpdatedAt = now
	return true
}
