package tuning

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
)

func boolp(b bool) *bool { return &b }
func intp(n int) *int    { return &n }

// ── database ────────────────────────────────────────────────────────────────

func TestLoadProfiles_EmbeddedParses(t *testing.T) {
	db, err := LoadProfiles("")
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	p, ok := db.Lookup("gfx1201", "agentic-single")
	if !ok {
		t.Fatal("gfx1201/agentic-single missing from embedded db")
	}
	if !p.Verified {
		t.Error("gfx1201 should be verified")
	}
	if p.Flags.FlashAttn == nil || !*p.Flags.FlashAttn {
		t.Error("gfx1201 should recommend flash attention")
	}
	if p.Flags.Parallel == nil || *p.Flags.Parallel != 1 {
		t.Error("gfx1201 should recommend parallel 1")
	}
	if p.MTP == nil || !p.MTP.ApplyToMTPModels {
		t.Error("gfx1201 should enable MTP for MTP models")
	}
	if len(p.Sources) == 0 {
		t.Error("gfx1201 should cite sources")
	}
	// Conservative archs present but unverified.
	for _, gfx := range []string{"gfx1100", "gfx1030"} {
		cp, ok := db.Lookup(gfx, "agentic-single")
		if !ok {
			t.Fatalf("%s missing", gfx)
		}
		if cp.Verified {
			t.Errorf("%s should be unverified", gfx)
		}
		if cp.MTP != nil {
			t.Errorf("%s should not enable MTP unverified", gfx)
		}
	}
}

func TestLookup_EmptyUseCaseResolvesDefault(t *testing.T) {
	db, _ := LoadProfiles("")
	p, ok := db.Lookup("gfx1201", "")
	if !ok || p.UseCase != "agentic-single" {
		t.Fatalf("empty usecase should resolve default, got ok=%v uc=%q", ok, p.UseCase)
	}
}

func TestLoadProfiles_UserFileOverridesAndExtends(t *testing.T) {
	dir := t.TempDir()
	user := `
version: 1
usecases:
  batch:
    description: throughput
profiles:
  - gfx: gfx1201
    usecase: agentic-single
    verified: false
    flags: { flash_attn: false, parallel: 8 }
  - gfx: gfx1151
    usecase: agentic-single
    verified: true
    flags: { flash_attn: true, parallel: 1 }
`
	if err := os.WriteFile(filepath.Join(dir, UserProfilesFilename), []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	// Override replaced the shipped gfx1201 entry.
	p, _ := db.Lookup("gfx1201", "agentic-single")
	if p.Verified {
		t.Error("user override should have replaced gfx1201 (now unverified)")
	}
	if p.Flags.Parallel == nil || *p.Flags.Parallel != 8 {
		t.Error("user override parallel=8 not applied")
	}
	// New gfx added.
	if _, ok := db.Lookup("gfx1151", "agentic-single"); !ok {
		t.Error("user-added gfx1151 missing")
	}
	// New use-case merged.
	if _, ok := db.UseCases["batch"]; !ok {
		t.Error("user use-case 'batch' not merged")
	}
}

func TestLoadProfiles_MissingUserFileIsNotError(t *testing.T) {
	if _, err := LoadProfiles(t.TempDir()); err != nil {
		t.Fatalf("missing user file should not error: %v", err)
	}
}

// ── injector ──────────────────────────────────────────────────────────────

func TestApplyProfile_InjectsMissingRespectsExplicit(t *testing.T) {
	p := Profile{Flags: Flags{FlashAttn: boolp(true), Parallel: intp(1)}}

	got := ApplyProfile("llama-server -m x.gguf -ngl 99", p, false, nil)
	if !strings.Contains(got, "--flash-attn on") || !strings.Contains(got, "--parallel 1") {
		t.Errorf("expected fa+parallel injected, got %q", got)
	}

	// Explicit --parallel 4 must survive; fa still injected.
	got = ApplyProfile("llama-server --parallel 4 -m x.gguf", p, false, nil)
	if !strings.Contains(got, "--parallel 4") || strings.Contains(got, "--parallel 1") {
		t.Errorf("explicit --parallel 4 must win, got %q", got)
	}
	// -fa alias counts as present.
	got = ApplyProfile("llama-server -fa on -m x.gguf", p, false, nil)
	if strings.Contains(got, "--flash-attn") {
		t.Errorf("-fa alias should suppress --flash-attn, got %q", got)
	}
}

func TestApplyProfile_MTPOnlyForMTPModels(t *testing.T) {
	p := Profile{
		Flags: Flags{FlashAttn: boolp(true), Parallel: intp(1)},
		MTP:   &MTP{ApplyToMTPModels: true, DraftNMax: 3, DraftPMin: 0.5},
	}
	nonMTP := ApplyProfile("llama-server -m plain.gguf", p, false, nil)
	if strings.Contains(nonMTP, "draft-mtp") {
		t.Errorf("non-MTP model must not get spec flags, got %q", nonMTP)
	}
	mtp := ApplyProfile("llama-server -m x.gguf", p, true, nil)
	if !strings.Contains(mtp, "--spec-type draft-mtp") || !strings.Contains(mtp, "--spec-draft-n-max 3") || !strings.Contains(mtp, "--draft-p-min 0.5") {
		t.Errorf("MTP model should get spec flags, got %q", mtp)
	}
}

func TestApplyProfile_Idempotent(t *testing.T) {
	p := Profile{
		Flags: Flags{FlashAttn: boolp(true), Parallel: intp(1)},
		MTP:   &MTP{ApplyToMTPModels: true, DraftNMax: 3, DraftPMin: 0.5},
	}
	once := ApplyProfile("llama-server -m x.gguf", p, true, []string{"-ub", "2048"})
	twice := ApplyProfile(once, p, true, []string{"-ub", "2048"})
	if once != twice {
		t.Errorf("not idempotent:\n once=%q\n twice=%q", once, twice)
	}
	if !strings.Contains(once, "-ub 2048") {
		t.Errorf("extra_args not appended, got %q", once)
	}
}

// ── override / resolve ──────────────────────────────────────────────────────

func TestResolve_DisabledInjectsNothing(t *testing.T) {
	base := Profile{Flags: Flags{FlashAttn: boolp(true), Parallel: intp(1)}}
	eff := Resolve(base, Override{Enabled: boolp(false)})
	if eff.Enabled {
		t.Fatal("Enabled=false should resolve disabled")
	}
}

func TestResolve_OverrideForcesValueAndProvenance(t *testing.T) {
	base := Profile{Flags: Flags{FlashAttn: boolp(true), Parallel: intp(1)}, MTP: &MTP{ApplyToMTPModels: true}}

	eff := Resolve(base, Override{FlashAttn: boolp(false), MTP: boolp(false)})
	if eff.Profile.Flags.FlashAttn == nil || *eff.Profile.Flags.FlashAttn {
		t.Error("override flash_attn=false should force off")
	}
	if eff.Source["flash_attn"] != FromOverride {
		t.Error("flash_attn provenance should be override")
	}
	if eff.Source["parallel"] != FromProfile {
		t.Error("untouched parallel provenance should be recommended")
	}
	if eff.Profile.MTP != nil {
		t.Error("override mtp=false should disable MTP")
	}
}

func TestResolve_ForceEnableMTPOnUnverified(t *testing.T) {
	base := Profile{Flags: Flags{FlashAttn: boolp(true)}} // no MTP recommended
	eff := Resolve(base, Override{MTP: boolp(true)})
	if eff.Profile.MTP == nil || !eff.Profile.MTP.ApplyToMTPModels {
		t.Error("override mtp=true should force-enable MTP even when profile omits it")
	}
}

// ── detection ───────────────────────────────────────────────────────────────

func writeCard(t *testing.T, root, card, vendor, device string) {
	t.Helper()
	dev := filepath.Join(root, "class", "drm", card, "device")
	if err := os.MkdirAll(dev, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dev, "vendor"), []byte(vendor+"\n"), 0o644)
	os.WriteFile(filepath.Join(dev, "device"), []byte(device+"\n"), 0o644)
}

func TestDetectGfx(t *testing.T) {
	cases := []struct {
		device string
		want   string
	}{
		{"0x7551", "gfx1201"}, // R9700
		{"0x7449", "gfx1100"}, // W7800
		{"0x744c", "gfx1100"}, // 7900 XTX
		{"0x73bf", "gfx1030"}, // 6800 XT
	}
	for _, tc := range cases {
		root := t.TempDir()
		writeCard(t, root, "card0", "0x1002", tc.device)
		gfx, id, ok := DetectGfx(root)
		if !ok || gfx != tc.want {
			t.Errorf("device %s: got (%q,%v), want %s", tc.device, gfx, ok, tc.want)
		}
		if id == 0 {
			t.Errorf("device %s: id not parsed", tc.device)
		}
	}
}

func TestDetectGfx_UnknownAndNonAMD(t *testing.T) {
	root := t.TempDir()
	writeCard(t, root, "card0", "0x10de", "0x2204") // NVIDIA — skipped
	writeCard(t, root, "card1", "0x1002", "0xdead") // AMD but unknown id
	if _, _, ok := DetectGfx(root); ok {
		t.Error("unknown AMD id + non-AMD card should yield ok=false")
	}
}

// ── MTP capability ──────────────────────────────────────────────────────────

func TestIsMTPModel(t *testing.T) {
	if !IsMTPModel("llama-server -m /m/plain.gguf", true) {
		t.Error("metadata flag should force MTP true")
	}
	if !IsMTPModel("llama-server --model /m/Qwopus-27B-v2-MTP-Q8_0.gguf", false) {
		t.Error("filename with -MTP- should be detected")
	}
	if IsMTPModel("llama-server --model /m/Qwen3.6-35B-A3B-Q8_0.gguf", false) {
		t.Error("plain model must not be detected as MTP")
	}
	if IsMTPModel("llama-server --model /mtparty/plain.gguf", false) {
		t.Error("substring 'mtp' inside another word must not match")
	}
}

// ── backend env (glibc allocator caps) ───────────────────────────────────────

func TestBackendEnv_EmbeddedParses(t *testing.T) {
	db, _ := LoadProfiles("")
	want := map[string]string{"MALLOC_MMAP_THRESHOLD_": "65536", "MALLOC_TRIM_THRESHOLD_": "65536"}
	for k, v := range want {
		if got := db.BackendEnv.LinuxGlibc[k]; got != v {
			t.Errorf("backend_env %s = %q, want %q", k, got, v)
		}
	}
}

func TestBackendEnvToInject_Resolution(t *testing.T) {
	db, _ := LoadProfiles("")
	cases := []struct {
		name    string
		isLinux bool
		tc      *config.TuningConfig
		wantLen int
	}{
		{"linux default", true, nil, 2},
		{"non-linux is no-op", false, nil, 0},
		{"globally disabled", true, &config.TuningConfig{Enabled: boolp(false)}, 0},
		{"backend_env toggle off", true, &config.TuningConfig{BackendEnv: boolp(false)}, 0},
		{"backend_env toggle on", true, &config.TuningConfig{BackendEnv: boolp(true)}, 2},
		{"flags disabled but env stays", true, &config.TuningConfig{FlashAttn: boolp(false)}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := db.backendEnvToInject(c.tc, c.isLinux)
			if len(got) != c.wantLen {
				t.Fatalf("got %d vars, want %d (%v)", len(got), c.wantLen, got)
			}
		})
	}
}

func TestBackendEnvToInject_ReturnsCopy(t *testing.T) {
	db, _ := LoadProfiles("")
	got := db.backendEnvToInject(nil, true)
	got["MALLOC_MMAP_THRESHOLD_"] = "mutated"
	if db.BackendEnv.LinuxGlibc["MALLOC_MMAP_THRESHOLD_"] == "mutated" {
		t.Error("BackendEnvFor must return a copy; the DB was mutated")
	}
}

func TestInjectEnvDefaults(t *testing.T) {
	defaults := map[string]string{"MALLOC_MMAP_THRESHOLD_": "65536", "MALLOC_TRIM_THRESHOLD_": "65536"}

	// Empty env: both added, deterministic (sorted) order.
	env, added := injectEnvDefaults(nil, defaults)
	if !added || len(env) != 2 {
		t.Fatalf("expected 2 added, got %v (added=%v)", env, added)
	}
	if env[0] != "MALLOC_MMAP_THRESHOLD_=65536" || env[1] != "MALLOC_TRIM_THRESHOLD_=65536" {
		t.Errorf("unsorted/incorrect output: %v", env)
	}

	// User-set var wins: present key is not overwritten, only the other added.
	env, added = injectEnvDefaults([]string{"MALLOC_TRIM_THRESHOLD_=131072"}, defaults)
	if !added || len(env) != 2 {
		t.Fatalf("expected user var kept + 1 added, got %v", env)
	}
	joined := strings.Join(env, " ")
	if !strings.Contains(joined, "MALLOC_TRIM_THRESHOLD_=131072") {
		t.Error("user MALLOC_TRIM_THRESHOLD_ override must be preserved")
	}
	if strings.Contains(joined, "MALLOC_TRIM_THRESHOLD_=65536") {
		t.Error("default must not be appended when the key is already present")
	}

	// Nothing to add when all present.
	if _, added := injectEnvDefaults([]string{"MALLOC_MMAP_THRESHOLD_=1", "MALLOC_TRIM_THRESHOLD_=1"}, defaults); added {
		t.Error("added should be false when all keys present")
	}
}

func TestApplyToConfig_BackendEnvAndBackendGating(t *testing.T) {
	db, _ := LoadProfiles("")
	envMap := db.backendEnvToInject(nil, true) // what a Linux host would inject

	// Simulate the Env injection ApplyToConfig performs per llamacpp model.
	llamaEnv, added := injectEnvDefaults([]string{"EXISTING=1"}, envMap)
	if !added || len(llamaEnv) != 3 {
		t.Fatalf("llamacpp model should gain 2 vars atop its own, got %v", llamaEnv)
	}

	// Global disable yields no env to inject.
	if got := db.backendEnvToInject(&config.TuningConfig{Enabled: boolp(false)}, true); got != nil {
		t.Errorf("tuning.enabled=false must inject no env, got %v", got)
	}
}
