package config

import (
	"errors"
	"testing"
)

func TestRuntimeState_StartsValid(t *testing.T) {
	rs := NewRuntimeState()
	valid, errMsg, staleSince := rs.Status()
	if !valid || errMsg != "" || staleSince != nil {
		t.Errorf("new RuntimeState should be valid with no error, got valid=%v err=%q staleSince=%v", valid, errMsg, staleSince)
	}
}

func TestRuntimeState_InvalidThenValid(t *testing.T) {
	rs := NewRuntimeState()
	rs.SetInvalid(errors.New("boom"))

	valid, errMsg, staleSince := rs.Status()
	if valid || errMsg != "boom" || staleSince == nil {
		t.Fatalf("want invalid with error+staleSince, got valid=%v err=%q staleSince=%v", valid, errMsg, staleSince)
	}

	rs.SetValid()
	valid, errMsg, staleSince = rs.Status()
	if !valid || errMsg != "" || staleSince != nil {
		t.Errorf("SetValid should clear invalid state, got valid=%v err=%q staleSince=%v", valid, errMsg, staleSince)
	}
}

func TestRuntimeState_StaleSinceDoesNotResetOnRepeatedFailure(t *testing.T) {
	rs := NewRuntimeState()
	rs.SetInvalid(errors.New("first"))
	_, _, first := rs.Status()

	rs.SetInvalid(errors.New("second"))
	_, errMsg, second := rs.Status()

	if errMsg != "second" {
		t.Errorf("want latest error message, got %q", errMsg)
	}
	if !first.Equal(*second) {
		t.Errorf("staleSince should not move on a repeated failure: first=%v second=%v", first, second)
	}
}

func TestRuntimeState_LastGoodRoundTripsAndIsCopied(t *testing.T) {
	rs := NewRuntimeState()
	original := []byte("models: {}\n")
	rs.SetLastGood(original)

	got := rs.LastGood()
	if string(got) != string(original) {
		t.Fatalf("want %q, got %q", original, got)
	}

	// mutating the caller's slice after SetLastGood must not affect the
	// stored copy — callers may reuse read buffers.
	original[0] = 'X'
	got2 := rs.LastGood()
	if got2[0] == 'X' {
		t.Error("RuntimeState must copy the bytes it is given, not alias them")
	}
}

func TestRuntimeState_PendingDefaultsToReload(t *testing.T) {
	rs := NewRuntimeState()
	actor, summary := rs.TakePending()
	if actor != "reload" || summary != "" {
		t.Errorf("with nothing staged, want (\"reload\", \"\"), got (%q, %q)", actor, summary)
	}
}

func TestRuntimeState_PendingIsConsumedOnce(t *testing.T) {
	rs := NewRuntimeState()
	rs.SetPending("api:patch-model", "ctx_size -> 65536")

	actor, summary := rs.TakePending()
	if actor != "api:patch-model" || summary != "ctx_size -> 65536" {
		t.Fatalf("unexpected pending: (%q, %q)", actor, summary)
	}

	// a second take without a new SetPending falls back to the default —
	// staging is consumed, not sticky.
	actor2, summary2 := rs.TakePending()
	if actor2 != "reload" || summary2 != "" {
		t.Errorf("pending should be cleared after being taken, got (%q, %q)", actor2, summary2)
	}
}
