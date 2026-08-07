package server

import (
	"io"
	"strings"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/logmon"
	"github.com/androidand/llama-skein/internal/offload"
	"github.com/androidand/llama-skein/internal/placement"
	"github.com/androidand/llama-skein/internal/process"
	"github.com/androidand/llama-skein/internal/router"
)

// retryTestServer wires a Server whose model was auto-placed hybrid and whose
// last load failed with the given class.
func retryTestServer(t *testing.T, class process.FailureClass, msg string) (*Server, *stubRouter) {
	t.Helper()
	orig := "llama-server --port 9000 -m /models/big.gguf --ctx-size 32768"
	stub := newStubRouter([]string{"big"}, "")
	if class != "" {
		stub.modelErrors = map[string]*process.LoadError{
			"big": {Message: msg, Class: class},
		}
	}
	s := &Server{
		cfg: config.Config{
			Models: map[string]config.ModelConfig{
				"big": {Cmd: orig + " --n-cpu-moe 30"},
			},
		},
		proxylog:         logmon.NewWriter(io.Discard),
		local:            stub,
		placementRetries: newPlacementRetry(),
		placements: map[string]placementRecord{
			"big": {
				OriginalCmd: orig,
				Plan: placement.Plan{
					Mode: placement.ModeHybrid, Confident: true, NCpuMoe: 30,
					FlagOps: []offload.FlagOp{{Name: "--n-cpu-moe", Value: "30"}},
				},
			},
		},
	}
	var _ router.LocalRouter = stub
	return s, stub
}

// A GPU OOM on an auto-placed model installs a safer command for the next
// start. The model file does not exist, so the planner cannot re-plan from
// GGUF metadata — the ladder must still make progress via the rungs that
// don't need it.
func TestEscalate_MemoryFailureInstallsOverride(t *testing.T) {
	s, stub := retryTestServer(t, process.ClassGPUOOM, "hipErrorOutOfMemory")
	s.escalateIfMemoryFailure("big")

	cmd, ok := stub.cmdOverrides["big"]
	if !ok {
		t.Fatal("a memory-class failure must install a retry command")
	}
	if cmd == "" {
		t.Fatal("override must not be empty")
	}
	if attempts := s.retryAttempts("big"); len(attempts) != 1 {
		t.Fatalf("expected 1 recorded attempt, got %d", len(attempts))
	}
}

// A non-memory failure must never trigger a placement retry — no placement
// rescues an unsupported architecture or a missing shard.
func TestEscalate_NonMemoryFailureIsNotRetried(t *testing.T) {
	for _, class := range []process.FailureClass{
		process.ClassUnsupportedArch, process.ClassMissingShard,
		process.ClassInvalidFlag, process.ClassBackendError, process.ClassCrashOther,
	} {
		s, stub := retryTestServer(t, class, "boom")
		s.escalateIfMemoryFailure("big")
		if len(stub.cmdOverrides) != 0 {
			t.Fatalf("class %q must not be retried", class)
		}
	}
}

// No failure at all: nothing to escalate.
func TestEscalate_NoFailureIsNoOp(t *testing.T) {
	s, stub := retryTestServer(t, "", "")
	s.escalateIfMemoryFailure("big")
	if len(stub.cmdOverrides) != 0 {
		t.Fatal("a model that has not failed must not be re-placed")
	}
}

// A model we did not place (operator-pinned, or out of scope) is left alone
// even when it fails for memory.
func TestEscalate_UnplannedModelIsNotRetried(t *testing.T) {
	s, stub := retryTestServer(t, process.ClassGPUOOM, "oom")
	delete(s.placements, "big")
	s.escalateIfMemoryFailure("big")
	if len(stub.cmdOverrides) != 0 {
		t.Fatal("a model automatic placement never touched must not be retried")
	}
}

// The ladder is bounded: after the budget is spent, no further overrides are
// installed no matter how many times the model fails.
func TestEscalate_BoundedByRetryBudget(t *testing.T) {
	s, _ := retryTestServer(t, process.ClassGPUOOM, "oom-1")
	s.cfg.Placement = config.PlacementConfig{MaxRetries: 2}

	for i := range 6 {
		// Each escalation must be a response to a *distinct* failure.
		s.local.(*stubRouter).modelErrors["big"] = &process.LoadError{
			Message: strings.Repeat("oom-", i+1), Class: process.ClassGPUOOM,
		}
		s.escalateIfMemoryFailure("big")
	}
	if got := len(s.retryAttempts("big")); got > 2 {
		t.Fatalf("recorded %d attempts, budget was 2", got)
	}
}

// A negative budget disables adaptive retry entirely.
func TestEscalate_DisabledByNegativeBudget(t *testing.T) {
	s, stub := retryTestServer(t, process.ClassGPUOOM, "oom")
	s.cfg.Placement = config.PlacementConfig{MaxRetries: -1}
	s.escalateIfMemoryFailure("big")
	if len(stub.cmdOverrides) != 0 {
		t.Fatal("maxRetries < 0 must disable adaptive retry")
	}
}

// The same failure must not advance two rungs: escalation is per distinct
// failure, so a repeated read of one error does not burn the budget.
func TestEscalate_SameFailureDoesNotStackRungs(t *testing.T) {
	s, _ := retryTestServer(t, process.ClassGPUOOM, "identical-oom")
	s.escalateIfMemoryFailure("big")
	first := len(s.retryAttempts("big"))
	s.escalateIfMemoryFailure("big")
	s.escalateIfMemoryFailure("big")
	if got := len(s.retryAttempts("big")); got != first {
		t.Fatalf("attempts grew from %d to %d on the same failure", first, got)
	}
}

// A later, distinct failure continues the ladder from where it got to,
// rather than re-trying a placement that already proved too aggressive.
func TestEscalate_LadderContinuesAfterEarlierRung(t *testing.T) {
	s, _ := retryTestServer(t, process.ClassGPUOOM, "oom-1")
	s.escalateIfMemoryFailure("big")
	first := s.retryAttempts("big")
	if len(first) != 1 {
		t.Fatalf("precondition: expected 1 attempt, got %d", len(first))
	}

	s.local.(*stubRouter).modelErrors["big"] = &process.LoadError{
		Message: "oom-2", Class: process.ClassGPUOOM,
	}
	s.escalateIfMemoryFailure("big")

	second := s.retryAttempts("big")
	if len(second) != 2 {
		t.Fatalf("expected the ladder to advance, got %d attempts", len(second))
	}
	if second[0].Rung == second[1].Rung {
		t.Fatalf("ladder repeated rung %q instead of escalating", second[1].Rung)
	}
}
