package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/androidand/llama-skein/internal/placement"
)

// A stored profile must come back as a plan that reproduces the same
// command — otherwise "reuse what worked" reuses something else.
func TestPlanFromProfile_ReproducesFlags(t *testing.T) {
	p := placement.Profile{
		Mode: placement.ModeHybrid, NCpuMoe: 42, PerfClass: placement.PerfCPUBoundHybrid,
		PeakVRAMMB: 40_000, PeakHostMB: 50_000,
		FlagOps: []placement.FlagRec{
			{Name: "--n-cpu-moe", Value: "42"},
			{Name: "--fit-target", Value: "2458"},
			{Name: "--cpu-moe", Boolean: true},
		},
	}
	plan := planFromProfile(p)
	if plan.Mode != placement.ModeHybrid || plan.NCpuMoe != 42 || !plan.Confident {
		t.Fatalf("plan lost the profile's decision: %+v", plan)
	}
	if len(plan.FlagOps) != 3 {
		t.Fatalf("flag count = %d", len(plan.FlagOps))
	}
	var boolSeen bool
	for _, op := range plan.FlagOps {
		if op.Name == "--cpu-moe" {
			boolSeen = op.Boolean
		}
	}
	if !boolSeen {
		t.Fatal("boolean flags must survive the round trip")
	}
	// The reused plan reports the measured cost, not a fresh estimate.
	if plan.Estimate.GPUMB != 40_000 || plan.Estimate.HostMB != 50_000 {
		t.Fatalf("estimate = %+v, want the measured peaks", plan.Estimate)
	}

	cmd, err := applyFlagOps("llama-server -m /models/x.gguf", plan.FlagOps)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--n-cpu-moe 42", "--fit-target 2458", "--cpu-moe"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("rebuilt command %q missing %q", cmd, want)
		}
	}
}

// The engine identity must move when the binary is replaced — an upgraded
// llama-server is exactly the case where old memory measurements stop
// applying.
func TestEngineIdentity_ChangesWithBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "llama-server")
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := engineIdentity(bin)

	if err := os.WriteFile(bin, []byte("v2-is-longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if second := engineIdentity(bin); second == first {
		t.Fatal("replacing the engine binary must change its identity")
	}
	// A missing binary still yields a stable identity rather than crashing.
	if got := engineIdentity(filepath.Join(dir, "nope")); got == "" {
		t.Fatal("a missing binary must still produce an identity")
	}
}

// Regression (z4, 2026-08-07): the daemon runs without HOME in its
// environment, so no home directory resolved, the store was nil, and
// learning was silently disabled with no log line to say so. A missing home
// must fall back to the daemon state directory instead.
func TestPlacementProfilePaths_FallsBackWithoutHome(t *testing.T) {
	withHome := placementProfilePaths("/root")
	if len(withHome) != 2 || !strings.HasPrefix(withHome[0], "/root/") {
		t.Fatalf("with a home, that path must be preferred: %v", withHome)
	}
	noHome := placementProfilePaths("")
	if len(noHome) != 1 {
		t.Fatalf("without a home there must still be a candidate: %v", noHome)
	}
	if !strings.Contains(noHome[0], "llama-skein") {
		t.Fatalf("fallback path = %q", noHome[0])
	}
	// Both orderings must end at the same durable fallback.
	if withHome[len(withHome)-1] != noHome[0] {
		t.Fatal("the last-resort path must be the same either way")
	}
}

// Recording is skipped for models automatic placement never touched.
func TestRecordReadyPlacements_SkipsUnplannedModels(t *testing.T) {
	s := placementTestServer(nil)
	s.placementProfiles = placement.NewProfileStore(filepath.Join(t.TempDir(), "p.json"))
	stub := newStubRouter([]string{"m"}, "")
	s.local = stub
	// No entry in s.placements at all: nothing to learn.
	s.recordReadyPlacements()

	// And a recorded-but-not-applied (full-GPU) plan is equally not learned:
	// there is no placement decision to reuse.
	s.placements["m"] = placementRecord{Plan: placement.Plan{Mode: placement.ModeGPU}}
	s.recordReadyPlacements()
	if _, ok := s.placementProfiles.Lookup("m", placement.Key{}); ok {
		t.Fatal("a model we did not place must never be recorded")
	}

}
