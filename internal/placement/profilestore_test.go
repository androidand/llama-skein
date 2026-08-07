package placement

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/offload"
)

func testKey() Key {
	return Key{
		ModelPath: "/models/big.gguf", ModelSize: 91 << 30, ModelMtime: 1700000000,
		Engine: "llama-b1297", VRAMTotal: 48 * 1024, HostTotal: 112 * 1024, Ctx: 32768,
	}
}

func hybridPlan() Plan {
	return Plan{
		Mode: ModeHybrid, Confident: true, NCpuMoe: 42, PerfClass: PerfCPUBoundHybrid,
		FlagOps: []offload.FlagOp{
			{Name: "--n-cpu-moe", Value: "42"},
			{Name: "--fit-target", Value: "2458"},
		},
	}
}

func TestProfileStore_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "placements.json")
	store := NewProfileStore(path)
	key := testKey()

	if _, ok := store.Lookup("big", key); ok {
		t.Fatal("an empty store must report no profile")
	}

	policy := config.PlacementConfig{}
	p, ok := ProfileFrom(key, hybridPlan(), 40_000, 60_000, 180, 1700000100, policy)
	if !ok {
		t.Fatal("a comfortable run must be worth learning")
	}
	if err := store.Record("big", p); err != nil {
		t.Fatal(err)
	}

	got, ok := store.Lookup("big", key)
	if !ok {
		t.Fatal("the recorded profile must be found under the same key")
	}
	if got.NCpuMoe != 42 || got.Mode != ModeHybrid || len(got.FlagOps) != 2 {
		t.Fatalf("round-trip lost detail: %+v", got)
	}
	if got.PeakVRAMMB != 40_000 || got.LoadSeconds != 180 {
		t.Fatalf("measurements lost: %+v", got)
	}
}

// Every keyed input invalidates the profile when it changes — the whole
// point is that a profile is only valid for the world it was measured in.
func TestProfileStore_InvalidatedByEveryKeyInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "placements.json")
	store := NewProfileStore(path)
	key := testKey()
	p, _ := ProfileFrom(key, hybridPlan(), 40_000, 60_000, 180, 1, config.PlacementConfig{})
	if err := store.Record("big", p); err != nil {
		t.Fatal(err)
	}

	mutations := map[string]func(*Key){
		"model re-downloaded (size)":  func(k *Key) { k.ModelSize += 1 << 20 },
		"model rebuilt (mtime)":       func(k *Key) { k.ModelMtime++ },
		"different weights path":      func(k *Key) { k.ModelPath = "/models/other.gguf" },
		"engine upgraded":             func(k *Key) { k.Engine = "llama-b1400" },
		"different card":              func(k *Key) { k.VRAMTotal = 24 * 1024 },
		"container limit raised":      func(k *Key) { k.HostTotal = 48 * 1024 },
		"different context requested": func(k *Key) { k.Ctx = 65536 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := testKey()
			mutate(&changed)
			if _, ok := store.Lookup("big", changed); ok {
				t.Fatalf("%s must invalidate the profile", name)
			}
		})
	}
	// Unchanged key still hits.
	if _, ok := store.Lookup("big", testKey()); !ok {
		t.Fatal("the unchanged key must still match")
	}
}

// A run that only just fit must not become the starting point for every
// future launch.
func TestWorthLearning_RejectsBarelyFit(t *testing.T) {
	key := testKey()
	policy := config.PlacementConfig{}
	reserve := policy.GpuReserveMB(key.VRAMTotal)

	// Comfortably inside the reserve: learn it.
	if !WorthLearning(hybridPlan(), key.VRAMTotal-reserve-1000, 0, key, policy) {
		t.Fatal("a run with headroom must be worth learning")
	}
	// Peak ate into the reserve: do not learn it.
	if WorthLearning(hybridPlan(), key.VRAMTotal-reserve+1, 0, key, policy) {
		t.Fatal("a run that breached the GPU reserve must not be learned")
	}
	// Host side breached.
	hostReserve := policy.HostReserveMB(key.HostTotal)
	if WorthLearning(hybridPlan(), 10_000, key.HostTotal-hostReserve+1, key, policy) {
		t.Fatal("a run that breached the host reserve must not be learned")
	}
	// No measurement at all: nothing to verify, so nothing to learn.
	if WorthLearning(hybridPlan(), 0, 0, key, policy) {
		t.Fatal("an unmeasured run must not be learned")
	}
	// Never learn a refusal or an unconfident plan.
	refuse := hybridPlan()
	refuse.Mode = ModeRefuse
	if WorthLearning(refuse, 10_000, 0, key, policy) {
		t.Fatal("a refusal must not be learned")
	}
	unconfident := hybridPlan()
	unconfident.Confident = false
	if WorthLearning(unconfident, 10_000, 0, key, policy) {
		t.Fatal("an unconfident plan must not be learned")
	}
}

func TestProfileStore_Forget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "placements.json")
	store := NewProfileStore(path)
	key := testKey()
	p, _ := ProfileFrom(key, hybridPlan(), 40_000, 0, 10, 1, config.PlacementConfig{})
	if err := store.Record("big", p); err != nil {
		t.Fatal(err)
	}
	store.Forget("big")
	if _, ok := store.Lookup("big", key); ok {
		t.Fatal("a forgotten profile must not be returned")
	}
}

// A corrupt or unreadable store must never break planning — it just means
// no profile is available.
func TestProfileStore_CorruptFileFailsOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "placements.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewProfileStore(path)
	if _, ok := store.Lookup("big", testKey()); ok {
		t.Fatal("a corrupt store must report no profile rather than a bad one")
	}
	// Recording over a corrupt file must still work (it is replaced).
	p, _ := ProfileFrom(testKey(), hybridPlan(), 40_000, 0, 10, 1, config.PlacementConfig{})
	if err := store.Record("big", p); err != nil {
		t.Fatalf("recording over a corrupt store failed: %v", err)
	}
	if _, ok := store.Lookup("big", testKey()); !ok {
		t.Fatal("the freshly recorded profile must be readable")
	}
}

// A nil store is a no-op, so callers need no nil checks.
func TestProfileStore_NilIsSafe(t *testing.T) {
	var store *ProfileStore
	if _, ok := store.Lookup("big", testKey()); ok {
		t.Fatal("a nil store must report no profile")
	}
	if err := store.Record("big", Profile{}); err != nil {
		t.Fatalf("a nil store must accept Record silently: %v", err)
	}
	store.Forget("big")
}
