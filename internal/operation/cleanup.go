package operation

import (
	"fmt"
	"os"
)

// CleanupAbandonedPartials removes ".part" files belonging to every
// PhaseCancelled operation in store — design.md decision 4: "Cancellation
// removes partials only when explicitly requested by policy. A separate
// cleanup operation handles abandoned partials." Cancellation itself
// (Operation.Cancel, wired through internal/server's
// handleAPICancelModelOperation) never deletes anything; this function is
// that separate, deliberate cleanup step.
//
// Only PhaseCancelled operations are considered — not PhaseFailed ones.
// A failed operation's partial can still be genuinely resumable (task 4.3's
// downloadOne resumes from any ".part" file regardless of why the previous
// attempt stopped, e.g. a transient network error), so removing it here
// would foreclose a legitimate future retry with a new operation targeting
// the same destination. A cancelled operation, by contrast, was stopped on
// purpose and nothing currently in this change resubmits it automatically.
//
// modelsDir must match the value the Executor that ran these operations
// used (Executor.ModelsDir) so the composed destination paths match what
// downloadOne actually wrote to disk.
//
// Called (with Store.Prune) from the server's reclaimOperationStorage once
// an operation reaches a terminal phase. There is no periodic scheduler and
// no OpenAPI route for triggering a pass on demand.
//
// Returns the number of partial files actually removed. A missing partial
// (already cleaned up, or the download never got far enough to create one)
// is not an error.
func CleanupAbandonedPartials(store *Store, modelsDir string) (int, error) {
	ops, err := store.List()
	if err != nil {
		return 0, fmt.Errorf("operation: cleanup: %w", err)
	}
	removed := 0
	for _, op := range ops {
		if op.Phase != PhaseCancelled {
			continue
		}
		for _, a := range op.Artifacts {
			dest, err := ResolveArtifactDestination(modelsDir, op.SourceRepository, a.Path)
			if err != nil {
				// An operation whose repository/path no longer resolves
				// safely (e.g. modelsDir changed since it ran) has nothing
				// this function can safely locate and remove — skip it
				// rather than guess at a path.
				continue
			}
			if rmErr := os.Remove(dest + ".part"); rmErr == nil {
				removed++
			} else if !os.IsNotExist(rmErr) {
				return removed, fmt.Errorf("operation: cleanup %s: %w", op.ID, rmErr)
			}
		}
	}
	return removed, nil
}
