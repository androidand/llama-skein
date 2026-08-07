package server

import (
	"sync"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/placement"
)

// retryState is the ladder history for one model: which rungs have been
// tried, what each attempt was, and the plan currently in force.
type retryState struct {
	rungs     []placement.Rung
	attempts  []placement.Attempt
	current   placement.Plan
	exhausted bool
}

// placementRetry holds per-model ladder state. It lives on the Server, so it
// resets on a config reload — a reload re-plans from scratch, which is the
// correct starting point for a changed config.
type placementRetry struct {
	mu    sync.Mutex
	state map[string]*retryState
}

func newPlacementRetry() *placementRetry {
	return &placementRetry{state: map[string]*retryState{}}
}

// ensurePlacement re-plans a model whose boot-time placement could not be
// decided, and installs the result for its next start.
//
// This exists because applyAutoPlacement runs inside New(), before the perf
// monitor has produced its first sample: at that moment VRAM telemetry is
// unknown, the planner correctly refuses to guess, and every model would be
// left unplanned forever. By the time a load is actually requested the
// telemetry is warm, so the decision can be made properly — and applied via
// the same command override the retry ladder uses, since the router has
// already captured its configs by then.
//
// A model that was planned successfully at boot is untouched.
func (s *Server) ensurePlacement(modelID string) {
	if s.local == nil || s.placements == nil {
		return
	}
	realName, found := s.cfg.RealModelName(modelID)
	if !found {
		realName = modelID
	}

	s.placementMu.Lock()
	defer s.placementMu.Unlock()

	rec, known := s.placements[realName]
	if known && rec.Plan.Mode != placement.ModeUnknown {
		return // already decided
	}
	mc, ok := s.cfg.Models[realName]
	if !ok {
		return
	}
	// Plan from the ORIGINAL command when we have one, so a re-plan never
	// composes onto flags a previous plan added.
	orig := mc
	if known && rec.OriginalCmd != "" {
		orig.Cmd = rec.OriginalCmd
	}
	fresh, ok := s.planModel(realName, orig)
	if !ok || fresh.Plan.Mode == placement.ModeUnknown {
		return // still cannot decide; leave the model exactly as configured
	}

	s.placements[realName] = fresh
	switch {
	case fresh.Plan.Mode == placement.ModeRefuse && fresh.Plan.Confident:
		s.unfittable[realName] = fresh.Plan.Reason
		s.proxylog.Warnf("placement: model %q cannot be placed within safe memory budgets — refusing rather than loading. %s",
			realName, fresh.Plan.Reason)
	case fresh.Plan.Applies():
		newCmd, err := applyFlagOps(orig.Cmd, fresh.Plan.FlagOps)
		if err != nil {
			s.proxylog.Warnf("placement: model %q deferred plan not applied: %v", realName, err)
			return
		}
		deadline := placement.LoadDeadlineSeconds(fresh.WeightBytes, fresh.Plan.Mode)
		if !s.local.SetCommandOverride(realName, newCmd, deadline) {
			return
		}
		fresh.AppliedCmd = newCmd
		s.placements[realName] = fresh
		// The placement IS the remedy that makes this model loadable, so a
		// stale "cannot fit" verdict recorded against the unplaced command
		// must not keep refusing it. Dropping the entry here is what makes
		// hybrid placement the fit guard's third remedy in the deferred
		// path, alongside shrinking the context and refusing outright.
		delete(s.unfittable, realName)
		s.maxSafeCtxCache.Delete(realName)
		s.proxylog.Infof("placement: model %q planned %s (%s) on first load: %s",
			realName, fresh.Plan.Mode, fresh.Plan.PerfClass, fresh.Plan.Reason)

		// Cross-check the planned command against the engine's own allocator
		// in the background. Boot-time planning is usually deferred to here
		// (VRAM telemetry is not up yet inside New), so without this the
		// preflight never runs at all in practice. It must not run inline:
		// this is the load path, and it holds placementMu.
		placed := mc
		placed.Cmd = newCmd
		go s.recordPreflight(realName, placed)
	}
}

// recordPreflight runs the llama-fit-params cross-check and stores its output
// on the placement record. Advisory only — a missing or failing tool leaves
// the record as it is.
func (s *Server) recordPreflight(realName string, mc config.ModelConfig) {
	out, ok := s.preflightFitParams(mc)
	if !ok {
		return
	}
	s.placementMu.Lock()
	defer s.placementMu.Unlock()
	rec, exists := s.placements[realName]
	if !exists {
		return
	}
	rec.EffectiveArgs = out
	s.placements[realName] = rec
}

// escalateIfMemoryFailure is the adaptive-retry entry point, called on the
// load path just before a NOT-resident model would be launched. When that
// model's last failure was memory-related and its placement was planned
// automatically, it installs the next, more conservative command so the
// attempt about to happen is a better one than the attempt that just failed.
//
// Retry is deliberately "the next attempt is smarter" rather than an
// immediate relaunch loop: the caller is already driving a load, so there is
// nothing to re-drive, and it inherits the crash-loop breaker's pacing for
// free instead of racing it.
//
// Everything here fails open — no plan, a non-memory failure class, an
// exhausted ladder, or a model the router does not know all leave the
// command exactly as it is.
func (s *Server) escalateIfMemoryFailure(modelID string) {
	if s.placementRetries == nil || s.local == nil {
		return
	}
	budget := s.cfg.Placement.RetryBudget()
	if budget <= 0 {
		return
	}

	realName, found := s.cfg.RealModelName(modelID)
	if !found {
		realName = modelID
	}
	rec, planned := s.placements[realName]
	// Only models WE placed are retried: an operator-pinned command is
	// theirs to own, and a model we never planned has no ladder to walk.
	if !planned || !rec.Plan.Applies() {
		return
	}

	lastErr := s.local.ModelErrors()[realName]
	if lastErr == nil || !lastErr.Class.IsMemory() {
		return
	}

	s.placementRetries.mu.Lock()
	defer s.placementRetries.mu.Unlock()

	st, ok := s.placementRetries.state[realName]
	if !ok {
		st = &retryState{current: rec.Plan}
		s.placementRetries.state[realName] = st
	}
	if st.exhausted || len(st.rungs) >= budget {
		return
	}
	// One escalation per distinct failure: if the command already in force
	// is the one we installed for the previous rung and it has not been
	// tried since, don't stack another rung on top of the same failure.
	if len(st.attempts) > 0 && st.attempts[len(st.attempts)-1].Failure == lastErr.Message {
		return
	}

	in := s.placementInputs(realName)
	next, rung, ok := placement.NextSaferPlan(st.current, in, string(lastErr.Class), st.rungs)
	if !ok {
		st.exhausted = true
		s.proxylog.Warnf("placement retry: model %q exhausted the ladder after %d attempt(s) — last failure (%s): %s",
			realName, len(st.rungs), lastErr.Class, lastErr.Message)
		return
	}

	newCmd, err := applyFlagOps(rec.OriginalCmd, next.FlagOps)
	if err != nil {
		s.proxylog.Warnf("placement retry: model %q could not build the %s command: %v", realName, rung, err)
		return
	}
	if !s.local.SetCommandOverride(realName, newCmd, placement.LoadDeadlineSeconds(rec.WeightBytes, next.Mode)) {
		return
	}

	st.rungs = append(st.rungs, rung)
	st.attempts = append(st.attempts, placement.Attempt{Rung: rung, Plan: next, Failure: lastErr.Message})
	st.current = next
	s.proxylog.Warnf("placement retry: model %q failed with %s; retrying at rung %q (attempt %d/%d): %s",
		realName, lastErr.Class, rung, len(st.rungs), budget, next.Reason)
}

// placementInputs rebuilds the planner inputs for a model, so a retry plans
// against memory as it is NOW rather than as it was at boot.
//
// When full inputs cannot be rebuilt (the weight file has become unreadable,
// say), it degrades to policy plus the configured context instead of giving
// up: with no model shape the rungs that need one (re-planning the expert
// split) decline on their own, leaving the metadata-free rungs — smaller
// batches, a shorter context — still available. A model whose GGUF we can no
// longer read is exactly a model we should still be able to make safer.
func (s *Server) placementInputs(realName string) placement.Inputs {
	fallback := placement.Inputs{Policy: s.cfg.Placement}
	mc, ok := s.cfg.Models[realName]
	if !ok {
		return fallback
	}
	rec, ok := s.placements[realName]
	if !ok {
		return fallback
	}
	// Plan against the command as CONFIGURED, not as previously rewritten:
	// the ladder composes its own flags onto the original.
	orig := mc
	orig.Cmd = rec.OriginalCmd
	if in, ok := s.planInputs(realName, orig); ok {
		return in
	}
	if args, err := orig.SanitizedCommand(); err == nil {
		if v, ok := commandFlagInt(args, "--ctx-size", "-c"); ok {
			fallback.ConfiguredCtx = v
		}
	}
	return fallback
}

// retryAttempts returns a copy of the ladder history for a model, for API
// reporting.
func (s *Server) retryAttempts(realName string) []placement.Attempt {
	if s.placementRetries == nil {
		return nil
	}
	s.placementRetries.mu.Lock()
	defer s.placementRetries.mu.Unlock()
	st, ok := s.placementRetries.state[realName]
	if !ok || len(st.attempts) == 0 {
		return nil
	}
	out := make([]placement.Attempt, len(st.attempts))
	copy(out, st.attempts)
	return out
}

// A successful load deliberately does NOT reset the ladder: the safer
// command that finally worked is the one the model should keep, and if it
// later fails again for memory the escalation should continue from where it
// got to rather than re-trying the placement that already proved too
// aggressive. Ladder state resets on config reload, which rebuilds the
// Server (and re-plans every model) from scratch.
