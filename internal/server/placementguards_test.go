package server

import (
	"io"
	"strings"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/logmon"
	"github.com/androidand/llama-skein/internal/perf"
	"github.com/androidand/llama-skein/internal/placement"
)

// A hybrid model legitimately produces the GPU-stall signature while its
// experts compute in host RAM — the watchdog must not treat it as wedged.
func TestPlacementIsHostHeavy(t *testing.T) {
	cases := []struct {
		name string
		mode placement.Mode
		cmd  string
		want bool
	}{
		{"planned hybrid", placement.ModeHybrid, "llama-server -m x.gguf --n-cpu-moe 20", true},
		{"planned cpu", placement.ModeCPU, "llama-server -m x.gguf --n-gpu-layers 0", true},
		{"planned gpu", placement.ModeGPU, "llama-server -m x.gguf", false},
		// No plan recorded (custom/pinned): fall back to reading the command.
		{"hand-pinned cpu-moe", "", "llama-server -m x.gguf --cpu-moe", true},
		{"hand-pinned n-cpu-moe", "", "llama-server -m x.gguf -ncmoe 12", true},
		{"hand-pinned override-tensor", "", "llama-server -m x.gguf -ot exps=CPU", true},
		{"hand-pinned ngl 0", "", "llama-server -m x.gguf -ngl 0", true},
		{"plain gpu command", "", "llama-server -m x.gguf -ngl 99", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Server{
				cfg:        config.Config{Models: map[string]config.ModelConfig{"m": {Cmd: c.cmd}}},
				placements: map[string]placementRecord{},
			}
			if c.mode != "" {
				s.placements["m"] = placementRecord{Plan: placement.Plan{Mode: c.mode}}
			}
			if got := s.placementIsHostHeavy("m"); got != c.want {
				t.Fatalf("placementIsHostHeavy = %v, want %v", got, c.want)
			}
		})
	}
}

func TestPlacementAdmissionRefusal(t *testing.T) {
	hostPlan := func(hostMB int) placement.Plan {
		return placement.Plan{Mode: placement.ModeHybrid, Confident: true,
			Estimate: placement.Estimate{HostMB: hostMB}}
	}
	// Memory figures come from the effective (cgroup-clamped) fields.
	st := func(availMB int) perf.SysStat {
		return perf.SysStat{MemAvailableMB: availMB, MemEffectiveAvailableMB: availMB, MemLimitSource: "cgroup-v2"}
	}

	if _, refuse := placementAdmissionRefusal(hostPlan(50_000), true, st(20_000)); !refuse {
		t.Fatal("a plan needing 50GB of host RAM with 20GB available must be refused")
	}
	if _, refuse := placementAdmissionRefusal(hostPlan(20_000), true, st(50_000)); refuse {
		t.Fatal("a plan that fits available memory must be admitted")
	}
	// Fail-open cases.
	if _, refuse := placementAdmissionRefusal(hostPlan(50_000), false, st(1)); refuse {
		t.Fatal("no plan recorded must fail open")
	}
	unconfident := hostPlan(50_000)
	unconfident.Confident = false
	if _, refuse := placementAdmissionRefusal(unconfident, true, st(1)); refuse {
		t.Fatal("an unconfident plan must fail open")
	}
	if _, refuse := placementAdmissionRefusal(hostPlan(0), true, st(1)); refuse {
		t.Fatal("a full-GPU plan (no host share) must fail open")
	}
	if _, refuse := placementAdmissionRefusal(hostPlan(50_000), true, perf.SysStat{}); refuse {
		t.Fatal("unreadable memory figures must fail open")
	}
}

// The timeout advisory must fire only for host-bandwidth-paced placements
// with a cap low enough to cut generation off — and must never mutate config.
func TestWarnSlowPlacementTimeouts_DoesNotMutateConfig(t *testing.T) {
	s := placementTestServer(map[string]config.ModelConfig{
		"slow": {Cmd: "llama-server -m x.gguf", MaxRequestTimeSecs: 60},
	})
	s.cfg.MaxRequestTimeSecs = 60
	s.placements["slow"] = placementRecord{
		Plan: placement.Plan{Mode: placement.ModeHybrid, PerfClass: placement.PerfCPUBoundHybrid},
	}
	s.warnSlowPlacementTimeouts()
	if s.cfg.Models["slow"].MaxRequestTimeSecs != 60 || s.cfg.MaxRequestTimeSecs != 60 {
		t.Fatal("the advisory must never change the operator's timeout")
	}
}

// Regression (z4 acceptance, 2026-08-07): a model rescued by hybrid
// placement was still refused with 507, because the fit guard judged the
// CONFIGURED command while the placement lived only on the process. The
// placement is the guard's third remedy — once it applies, the stale
// "cannot fit" verdict must not keep refusing the model.
func TestModelLoadRefusal_PlacementClearsStaleUnfittable(t *testing.T) {
	s := placementTestServer(map[string]config.ModelConfig{
		"big": {Cmd: "llama-server -m /models/big.gguf --ctx-size 32768"},
	})
	s.unfittable["big"] = "model + KV at this context exceeds VRAM"

	if _, refuse := s.modelLoadRefusal("big"); !refuse {
		t.Fatal("precondition: an unfittable model is refused")
	}

	// A viable hybrid placement takes effect.
	s.placementMu.Lock()
	delete(s.unfittable, "big")
	s.placements["big"] = placementRecord{
		OriginalCmd: "llama-server -m /models/big.gguf --ctx-size 32768",
		AppliedCmd:  "llama-server -m /models/big.gguf --ctx-size 32768 --n-cpu-moe 16",
		Plan:        placement.Plan{Mode: placement.ModeHybrid, Confident: true, NCpuMoe: 16},
	}
	s.placementMu.Unlock()

	if reason, refuse := s.modelLoadRefusal("big"); refuse {
		t.Fatalf("a hybrid-placed model must no longer be refused: %s", reason)
	}
	// And everything that judges the model must see the placed command.
	if cmd, ok := s.appliedCmd("big"); !ok || !strings.Contains(cmd, "--n-cpu-moe 16") {
		t.Fatalf("appliedCmd = %q, ok=%v", cmd, ok)
	}
}

// The deadline is a floor: it lifts a too-short timeout but never overrides
// an operator who set a longer one.
func TestRaiseLoadDeadline(t *testing.T) {
	const gb = int64(1) << 30
	log := logmon.NewWriter(io.Discard)

	// Default 120s against a 91 GB hybrid model: raised.
	raised := raiseLoadDeadline(120, 91*gb, placement.ModeHybrid, "big", log)
	if raised <= 120 {
		t.Fatalf("raised = %d, want more than the 120s that killed this load", raised)
	}
	// An operator's larger value wins.
	if got := raiseLoadDeadline(3000, 91*gb, placement.ModeHybrid, "big", log); got != 3000 {
		t.Fatalf("got %d, must never lower an operator's %d", got, 3000)
	}
	// Unknown weight size: leave the configured value alone.
	if got := raiseLoadDeadline(120, 0, placement.ModeGPU, "x", log); got != 120 {
		t.Fatalf("got %d, want the configured 120 when size is unknown", got)
	}
}
