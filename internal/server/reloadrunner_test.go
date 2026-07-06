package server

import (
	"sync"
	"testing"
	"time"
)

// A trigger during a run coalesces into exactly one follow-up pass, no matter
// how many triggers arrive.
func TestCoalescingRunner_TriggersDuringRunCoalesceToOneFollowUp(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	runs := 0

	trigger := NewCoalescingRunner(func() {
		mu.Lock()
		runs++
		first := runs == 1
		mu.Unlock()
		if first {
			started <- struct{}{}
			<-release
		}
	})

	done := make(chan struct{})
	go func() { trigger(); close(done) }()
	<-started

	// Three triggers land while the first pass is blocked.
	trigger()
	trigger()
	trigger()
	close(release)
	<-done

	mu.Lock()
	defer mu.Unlock()
	if runs != 2 {
		t.Fatalf("runs = %d, want 2 (initial + one coalesced follow-up)", runs)
	}
}

// A trigger during the follow-up pass dirties it again: loop-until-clean.
func TestCoalescingRunner_TriggerDuringFollowUpRunsThirdPass(t *testing.T) {
	var mu sync.Mutex
	runs := 0
	var trigger func()

	trigger = NewCoalescingRunner(func() {
		mu.Lock()
		runs++
		n := runs
		mu.Unlock()
		if n <= 2 {
			// Trigger from inside pass 1 and pass 2; each must cause one more.
			go trigger()
			time.Sleep(20 * time.Millisecond) // let the trigger land while running
		}
	})

	trigger()

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := runs
		mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("runs = %d, want >= 3 (trigger during follow-up must re-run)", n)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// Sequential triggers each run: no coalescing when nothing is in flight.
func TestCoalescingRunner_SequentialTriggersRunEachTime(t *testing.T) {
	var mu sync.Mutex
	runs := 0
	trigger := NewCoalescingRunner(func() {
		mu.Lock()
		runs++
		mu.Unlock()
	})

	trigger()
	trigger()
	trigger()

	mu.Lock()
	defer mu.Unlock()
	if runs != 3 {
		t.Fatalf("runs = %d, want 3", runs)
	}
}
