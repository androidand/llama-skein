package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/operation"
)

// newModelsTestServer wires a Server with one configured model ("my-model")
// whose weights path is under a real temp dir, and a real operation store
// the test can seed. This is apimodels.go's first dedicated test file —
// GET /api/models and GET /api/models/{model} had no coverage before
// task 5.1.
func newModelsTestServer(t *testing.T, weightsExist bool) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	weightsPath := filepath.Join(dir, "model.gguf")
	if weightsExist {
		if err := os.WriteFile(weightsPath, []byte("gguf bytes"), 0o644); err != nil {
			t.Fatalf("write weights file: %v", err)
		}
	}

	cfg := config.Config{
		Models: map[string]config.ModelConfig{
			"my-model": {
				Cmd: "llama-server -m " + weightsPath,
			},
		},
	}
	s := newTestServerWithConfig(cfg, newStubRouter(nil, ""), newStubRouter(nil, ""))
	store, err := operation.NewStore(t.TempDir(), 50)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	s.operationStore = store
	s.routes()
	return s, weightsPath
}

func getModelsList(t *testing.T, s *Server) []map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Models
}

func getModel(t *testing.T, s *Server, id string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/models/"+id, nil)
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

func TestHandleAPIListModels_BasicShape(t *testing.T) {
	s, _ := newModelsTestServer(t, true)
	models := getModelsList(t, s)
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	m := models[0]
	if m["id"] != "my-model" {
		t.Fatalf("id = %v, want my-model", m["id"])
	}
	if m["state"] != "stopped" {
		t.Fatalf("state = %v, want stopped", m["state"])
	}
	if m["loaded"] != false {
		t.Fatalf("loaded = %v, want false", m["loaded"])
	}
}

func TestHandleAPIListModels_InstalledReflectsFileExistence(t *testing.T) {
	present, _ := newModelsTestServer(t, true)
	models := getModelsList(t, present)
	if models[0]["installed"] != true {
		t.Fatalf("installed = %v, want true (weights file exists)", models[0]["installed"])
	}

	missing, _ := newModelsTestServer(t, false)
	models = getModelsList(t, missing)
	if models[0]["installed"] != false {
		t.Fatalf("installed = %v, want false (weights file does not exist)", models[0]["installed"])
	}
}

func TestHandleAPIListModels_ProvenanceOmittedWithNoMatchingOperation(t *testing.T) {
	s, _ := newModelsTestServer(t, true)
	models := getModelsList(t, s)
	m := models[0]
	for _, key := range []string{"source_repository", "source_revision", "artifact_paths", "active_operation_id"} {
		if _, present := m[key]; present {
			t.Fatalf("%s = %v, want omitted (no operation ever registered this model)", key, m[key])
		}
	}
}

func TestHandleAPIListModels_ProvenanceFromASucceededOperation(t *testing.T) {
	s, _ := newModelsTestServer(t, true)
	now := time.Now()
	op := operation.NewFromPlan(operation.Plan{
		SourceRepository: "org/repo-GGUF",
		SourceRevision:   "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		Artifacts: []operation.Artifact{
			{Path: "model.gguf", SizeBytes: 10, Role: operation.ArtifactRoleWeights},
			{Path: "tokenizer.json", SizeBytes: 5, Role: operation.ArtifactRoleTokenizer},
		},
		Registration: operation.Registration{ModelID: "my-model"},
	}, now)
	for _, phase := range []operation.Phase{
		operation.PhasePreflighting, operation.PhaseResolving, operation.PhaseDownloading,
		operation.PhaseVerifying, operation.PhaseInstalling, operation.PhaseRegistering,
		operation.PhaseReloading, operation.PhaseSucceeded,
	} {
		if err := op.TransitionTo(phase, now); err != nil {
			t.Fatalf("TransitionTo(%s): %v", phase, err)
		}
	}
	if err := s.operationStore.Save(op); err != nil {
		t.Fatalf("Save: %v", err)
	}

	m := getModelsList(t, s)[0]
	if m["source_repository"] != "org/repo-GGUF" {
		t.Fatalf("source_repository = %v, want org/repo-GGUF", m["source_repository"])
	}
	if m["source_revision"] != "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" {
		t.Fatalf("source_revision = %v, want the full SHA", m["source_revision"])
	}
	paths, ok := m["artifact_paths"].([]any)
	if !ok || len(paths) != 2 || paths[0] != "model.gguf" || paths[1] != "tokenizer.json" {
		t.Fatalf("artifact_paths = %v, want [model.gguf tokenizer.json]", m["artifact_paths"])
	}
	if _, present := m["active_operation_id"]; present {
		t.Fatalf("active_operation_id = %v, want omitted (this operation is terminal)", m["active_operation_id"])
	}
}

func TestHandleAPIListModels_ActiveOperationIDFromANonTerminalOperation(t *testing.T) {
	s, _ := newModelsTestServer(t, true)
	now := time.Now()
	op := operation.NewFromPlan(operation.Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []operation.Artifact{{Path: "model.gguf", SizeBytes: 10, Role: operation.ArtifactRoleWeights}},
		Registration:     operation.Registration{ModelID: "my-model"},
	}, now)
	if err := op.TransitionTo(operation.PhasePreflighting, now); err != nil {
		t.Fatalf("TransitionTo: %v", err)
	}
	if err := s.operationStore.Save(op); err != nil {
		t.Fatalf("Save: %v", err)
	}

	m := getModelsList(t, s)[0]
	if m["active_operation_id"] != op.ID {
		t.Fatalf("active_operation_id = %v, want %s", m["active_operation_id"], op.ID)
	}
	// Still in flight, so there is no succeeded provenance to report yet.
	if _, present := m["source_repository"]; present {
		t.Fatalf("source_repository = %v, want omitted (the operation hasn't succeeded)", m["source_repository"])
	}
}

func TestHandleAPIGetModel_HasTheSameProvenanceFields(t *testing.T) {
	s, _ := newModelsTestServer(t, true)
	now := time.Now()
	op := operation.NewFromPlan(operation.Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []operation.Artifact{{Path: "model.gguf", SizeBytes: 10, Role: operation.ArtifactRoleWeights}},
		Registration:     operation.Registration{ModelID: "my-model"},
	}, now)
	for _, phase := range []operation.Phase{
		operation.PhasePreflighting, operation.PhaseResolving, operation.PhaseDownloading,
		operation.PhaseVerifying, operation.PhaseInstalling, operation.PhaseRegistering,
		operation.PhaseReloading, operation.PhaseSucceeded,
	} {
		if err := op.TransitionTo(phase, now); err != nil {
			t.Fatalf("TransitionTo(%s): %v", phase, err)
		}
	}
	if err := s.operationStore.Save(op); err != nil {
		t.Fatalf("Save: %v", err)
	}

	m := getModel(t, s, "my-model")
	if m["installed"] != true {
		t.Fatalf("installed = %v, want true", m["installed"])
	}
	if m["source_repository"] != "org/repo" {
		t.Fatalf("source_repository = %v, want org/repo", m["source_repository"])
	}
}

func TestHandleAPIListModels_NoOperationStoreDoesNotBreakTheList(t *testing.T) {
	s, _ := newModelsTestServer(t, true)
	s.operationStore = nil // e.g. the state directory failed to initialize at startup.
	models := getModelsList(t, s)
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if models[0]["id"] != "my-model" {
		t.Fatalf("id = %v, want my-model", models[0]["id"])
	}
}
