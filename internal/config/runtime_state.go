package config

import (
	"sync"
	"time"
)

// RuntimeState is config-related state that must survive across hot
// reloads, even though the Server itself is rebuilt from scratch on every
// reload (see llama-skein.go's reloadPass). Created once at process startup
// and handed to every Server instance — initial and reloaded alike — via
// Server.SetRuntimeState; the pointer itself is never replaced.
//
// It exists for two related reasons:
//
//  1. Snapshotting needs the config content as it was *before* a reload,
//     but by the time a reload runs — whether triggered by an external file
//     edit, SIGHUP, or an API write — the previous content may already be
//     gone from disk (the whole point is defending against exactly that).
//     lastGood caches it in memory, updated only on a successful transition.
//
//  2. /health needs to report when the on-disk config is currently invalid,
//     which by construction cannot live on the Server built from that
//     config — there is no such Server.
type RuntimeState struct {
	mu sync.Mutex

	lastGood []byte

	valid      bool
	invalidErr string
	staleSince time.Time // zero when valid

	// pendingActor/pendingSummary are staged by an API handler immediately
	// before it triggers a reload, and consumed (read + cleared) by the
	// reload pass when it snapshots the outgoing config — attributing the
	// resulting history entry to what actually caused the change instead of
	// a generic "reload". Left empty for reloads with no such handler (an
	// external file edit, SIGHUP, or `-watch-config`).
	pendingActor   string
	pendingSummary string
}

// NewRuntimeState returns a RuntimeState considered valid until told
// otherwise.
func NewRuntimeState() *RuntimeState {
	return &RuntimeState{valid: true}
}

// LastGood returns the raw config bytes as of the last successful
// transition. Callers must not mutate the returned slice.
func (r *RuntimeState) LastGood() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastGood
}

// SetLastGood records data as the current known-good config content. The
// caller's slice is copied; RuntimeState owns its copy from this point.
func (r *RuntimeState) SetLastGood(data []byte) {
	cp := make([]byte, len(data))
	copy(cp, data)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastGood = cp
}

// SetValid marks the config as currently valid, clearing any prior failure.
func (r *RuntimeState) SetValid() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.valid = true
	r.invalidErr = ""
	r.staleSince = time.Time{}
}

// SetInvalid marks the config as currently invalid. staleSince is set once,
// the first time the config goes invalid, and held until SetValid clears it
// — a run of repeated failures should not keep resetting "how long has this
// been broken".
func (r *RuntimeState) SetInvalid(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.valid {
		r.staleSince = time.Now().UTC()
	}
	r.valid = false
	if err != nil {
		r.invalidErr = err.Error()
	}
}

// Status reports the current config validity for /health. staleSince is nil
// when valid.
func (r *RuntimeState) Status() (valid bool, errMsg string, staleSince *time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.valid {
		return true, "", nil
	}
	t := r.staleSince
	return false, r.invalidErr, &t
}

// SetPending stages the actor/summary attribution for the next reload's
// snapshot. Call immediately before triggering a reload from an API handler.
func (r *RuntimeState) SetPending(actor, summary string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pendingActor = actor
	r.pendingSummary = summary
}

// TakePending reads and clears the pending actor/summary, defaulting to
// ("reload", "") when nothing was staged.
func (r *RuntimeState) TakePending() (actor, summary string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	actor, summary = r.pendingActor, r.pendingSummary
	r.pendingActor, r.pendingSummary = "", ""
	if actor == "" {
		actor = "reload"
	}
	return actor, summary
}
