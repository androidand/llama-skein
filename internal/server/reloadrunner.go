package server

import "sync"

// NewCoalescingRunner returns a trigger func that runs fn at most once
// concurrently and never loses a trigger: a call arriving while fn runs marks
// the state dirty, and when the current pass finishes fn runs again — looping
// until a pass begins at-or-after the last trigger. Any number of triggers
// during one pass coalesce into a single follow-up pass.
//
// This exists for config reloads: a PATCH that lands after an in-flight
// reload's config read must still be applied by a subsequent reload, but N
// rapid patches must not queue N full reloads (each pass re-reads the whole
// config, so the last pass sees everything).
func NewCoalescingRunner(fn func()) func() {
	var mu sync.Mutex
	running := false
	dirty := false

	return func() {
		mu.Lock()
		if running {
			dirty = true
			mu.Unlock()
			return
		}
		running = true
		mu.Unlock()

		for {
			fn()
			mu.Lock()
			if !dirty {
				running = false
				mu.Unlock()
				return
			}
			dirty = false
			mu.Unlock()
		}
	}
}
