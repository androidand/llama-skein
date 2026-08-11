package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/operation"
)

// Task 5.4: writeModelToConfig and removeModelFromConfig gained a
// changed-bool no-op-detection return (same pattern patchModelInConfig
// already established), and registerInstalledModel gained both a missing
// SetPending call and use of that no-op signal. These tests are direct
// (no HTTP layer, no reload goroutine timing) precisely because
// triggerReload runs reloadFn in its own goroutine — asserting "no reload
// happened" by racing that goroutine would be exactly the kind of flaky
// test this session has already hit and fixed once (task 4.6's
// cancellation test). Testing the underlying changed bool and pending
// state directly sidesteps that entirely.

func newConfigWriteTestServer(t *testing.T, initialYAML string) *Server {
	t.Helper()
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgFile, []byte(initialYAML), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	s := newTestServerWithConfig(config.Config{}, newStubRouter(nil, ""), newStubRouter(nil, ""))
	s.configFile = cfgFile
	s.reloadFn = func() {}
	return s
}

func TestWriteModelToConfig_ReportsChangedOnARealWrite(t *testing.T) {
	s := newConfigWriteTestServer(t, "models: {}\n")
	changed, err := s.writeModelToConfig("m1", &config.ModelConfig{Cmd: "llama-server -m /a.gguf"})
	if err != nil {
		t.Fatalf("writeModelToConfig: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true for a genuinely new model entry")
	}
}

func TestWriteModelToConfig_ReportsUnchangedOnAnIdenticalRewrite(t *testing.T) {
	s := newConfigWriteTestServer(t, "models: {}\n")
	mc := &config.ModelConfig{Cmd: "llama-server -m /a.gguf"}
	if _, err := s.writeModelToConfig("m1", mc); err != nil {
		t.Fatalf("first writeModelToConfig: %v", err)
	}
	before, err := os.ReadFile(s.configFile)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}

	changed, err := s.writeModelToConfig("m1", mc)
	if err != nil {
		t.Fatalf("second writeModelToConfig: %v", err)
	}
	if changed {
		t.Fatal("changed = true, want false for an identical re-write")
	}
	after, err := os.ReadFile(s.configFile)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("config file was rewritten despite being a no-op:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestRemoveModelFromConfig_ReportsChangedWhenThePresentModelIsRemoved(t *testing.T) {
	s := newConfigWriteTestServer(t, "models:\n  m1:\n    cmd: \"llama-server -m /a.gguf\"\n")
	changed, err := s.removeModelFromConfig("m1")
	if err != nil {
		t.Fatalf("removeModelFromConfig: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true when the model was actually present and removed")
	}
}

func TestRemoveModelFromConfig_ReportsUnchangedWhenAlreadyAbsent(t *testing.T) {
	s := newConfigWriteTestServer(t, "models: {}\n")
	before, err := os.ReadFile(s.configFile)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}

	changed, err := s.removeModelFromConfig("never-existed")
	if err != nil {
		t.Fatalf("removeModelFromConfig: %v", err)
	}
	if changed {
		t.Fatal("changed = true, want false when the model was never present")
	}
	after, err := os.ReadFile(s.configFile)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("config file was rewritten despite being a no-op:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestRegisterInstalledModel_SetsPendingWithARealActorAndSummary(t *testing.T) {
	s := newConfigWriteTestServer(t, "models: {}\n")
	s.runtimeStateOrDefault() // ensure it's initialized before the assertion below drains it.

	op := operation.NewFromPlan(operation.Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []operation.Artifact{{Path: "model.gguf", SizeBytes: 10, Role: operation.ArtifactRoleWeights}},
		Registration:     operation.Registration{ModelID: "my-model", Backend: "llamacpp"},
	}, time.Now())

	if err := s.registerInstalledModel(op, "/models/model.gguf"); err != nil {
		t.Fatalf("registerInstalledModel: %v", err)
	}

	actor, summary := s.runtimeStateOrDefault().TakePending()
	if actor == "" || actor == "reload" {
		t.Fatalf("actor = %q, want a real attribution (the pre-5.4 bug: this call never staged one at all)", actor)
	}
	if summary == "" {
		t.Fatal("summary = \"\", want a real description of what was installed")
	}
}

func TestRegisterInstalledModel_SkipsPendingAndReloadOnAnIdenticalReRegistration(t *testing.T) {
	s := newConfigWriteTestServer(t, "models: {}\n")

	op := operation.NewFromPlan(operation.Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []operation.Artifact{{Path: "model.gguf", SizeBytes: 10, Role: operation.ArtifactRoleWeights}},
		Registration:     operation.Registration{ModelID: "my-model", Backend: "llamacpp"},
	}, time.Now())

	if err := s.registerInstalledModel(op, "/models/model.gguf"); err != nil {
		t.Fatalf("first registerInstalledModel: %v", err)
	}
	s.runtimeStateOrDefault().TakePending() // drain the first call's legitimate pending state.

	// task 4.7's idempotent-reinstall scenario: the exact same plan
	// resubmitted (e.g. a client retry) produces a fresh Operation with
	// identical Registration content, registered a second time.
	op2 := operation.NewFromPlan(operation.Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []operation.Artifact{{Path: "model.gguf", SizeBytes: 10, Role: operation.ArtifactRoleWeights}},
		Registration:     operation.Registration{ModelID: "my-model", Backend: "llamacpp"},
	}, time.Now())
	if err := s.registerInstalledModel(op2, "/models/model.gguf"); err != nil {
		t.Fatalf("second registerInstalledModel: %v", err)
	}

	// TakePending defaults an actor that was never staged to "reload" (its
	// own doc comment: "defaulting to (\"reload\", \"\") when nothing was
	// staged") — that default is exactly the signal this test is for.
	actor, summary := s.runtimeStateOrDefault().TakePending()
	if actor != "reload" || summary != "" {
		t.Fatalf(`actor/summary = %q/%q, want "reload"/"" (nothing staged) — an identical re-registration must not stage a new pending history entry or trigger a reload`, actor, summary)
	}
}

// TestHandleAPIConfigAddModel_IdenticalResubmitDoesNotRewriteTheFile is the
// handler-level confirmation of the same fix, exercised through the real
// HTTP route rather than writeModelToConfig directly. Checked via the
// config file's content, not a reload call count — triggerReload runs
// reloadFn in its own goroutine, and racing that goroutine to prove a
// negative ("no reload happened") is exactly the kind of flaky test this
// session already hit once (task 4.6) and deliberately avoids repeating.
func TestHandleAPIConfigAddModel_IdenticalResubmitDoesNotRewriteTheFile(t *testing.T) {
	s := newConfigWriteTestServer(t, "models: {}\n")
	body := `{"id":"m1","cmd":"llama-server -m /a.gguf"}`

	post := func() int {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/models/config", strings.NewReader(body)))
		return w.Code
	}

	if code := post(); code != http.StatusAccepted {
		t.Fatalf("first POST status = %d, want 202", code)
	}
	before, err := os.ReadFile(s.configFile)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}

	if code := post(); code != http.StatusAccepted {
		t.Fatalf("second (identical) POST status = %d, want 202", code)
	}
	after, err := os.ReadFile(s.configFile)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("config file was rewritten by an identical resubmit:\nbefore=%s\nafter=%s", before, after)
	}
}
