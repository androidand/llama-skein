package server

import (
	"encoding/json"
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

// newRemovalTestServer wires a Server whose model "my-model" points at a
// real weights file under modelsDir, with a real config file and operation
// store, for task 5.3's delete-endpoint tests.
func newRemovalTestServer(t *testing.T) (s *Server, modelsDir, weightsPath string) {
	t.Helper()
	modelsDir = t.TempDir()
	repoDir := filepath.Join(modelsDir, "org", "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	weightsPath = filepath.Join(repoDir, "model.gguf")
	if err := os.WriteFile(weightsPath, []byte("weights"), 0o644); err != nil {
		t.Fatalf("write weights file: %v", err)
	}

	cfgDir := t.TempDir()
	cfgFile := filepath.Join(cfgDir, "config.yaml")
	yaml := "models:\n  my-model:\n    cmd: \"llama-server -m " + weightsPath + "\"\n"
	if err := os.WriteFile(cfgFile, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	cfg := config.Config{
		ModelsDir: modelsDir,
		Models: map[string]config.ModelConfig{
			"my-model": {Cmd: "llama-server -m " + weightsPath},
		},
	}
	s = newTestServerWithConfig(cfg, newStubRouter([]string{"my-model"}, ""), newStubRouter(nil, ""))
	s.configFile = cfgFile
	s.reloadFn = func() {}
	store, err := operation.NewStore(t.TempDir(), 50)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	s.operationStore = store
	s.routes()
	return s, modelsDir, weightsPath
}

func deleteModel(t *testing.T, s *Server, id string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/models/"+id, nil))
	var body map[string]any
	if w.Body.Len() > 0 {
		json.Unmarshal(w.Body.Bytes(), &body) //nolint:errcheck
	}
	return w, body
}

func TestHandleAPIDeleteModel_HappyPathNoProvenance(t *testing.T) {
	s, _, weightsPath := newRemovalTestServer(t)
	local := s.local.(*stubRouter)

	w, body := deleteModel(t, s, "my-model")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if local.unloadCalls.Load() != 1 {
		t.Fatalf("unloadCalls = %d, want 1 (unload before delete)", local.unloadCalls.Load())
	}
	if _, err := os.Stat(weightsPath); !os.IsNotExist(err) {
		t.Fatalf("weights file should be deleted, stat err = %v", err)
	}
	if body["deleted"] != weightsPath {
		t.Fatalf(`body["deleted"] = %v, want %q (backward-compat key)`, body["deleted"], weightsPath)
	}
	if body["config_removed"] != true {
		t.Fatalf(`body["config_removed"] = %v, want true`, body["config_removed"])
	}
	// s.cfg itself isn't reloaded by this test (reloadFn is a no-op stub),
	// but the config *file* on disk must no longer mention the model.
	cfgBytes, err := os.ReadFile(s.configFile)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if strings.Contains(string(cfgBytes), "my-model") {
		t.Fatalf("config file still mentions my-model after delete:\n%s", cfgBytes)
	}
}

func TestHandleAPIDeleteModel_RefusesAPathOutsideModelsDir(t *testing.T) {
	s, modelsDir, _ := newRemovalTestServer(t)
	// Point the model's cmd at a file genuinely outside modelsDir.
	outside := filepath.Join(filepath.Dir(modelsDir), "outside.gguf")
	if err := os.WriteFile(outside, []byte("not owned by this models dir"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	s.cfg.Models = map[string]config.ModelConfig{
		"my-model": {Cmd: "llama-server -m " + outside},
	}

	w, _ := deleteModel(t, s, "my-model")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("file outside modelsDir must survive a refused delete: %v", err)
	}
}

func TestHandleAPIDeleteModel_RefusesWhenModelsDirUnknown(t *testing.T) {
	s, _, _ := newRemovalTestServer(t)
	s.cfg.ModelsDir = ""
	s.cfg.Models = map[string]config.ModelConfig{"my-model": {Cmd: "llama-server"}} // no -m/--model at all so modelsDir can't be inferred either.

	w, _ := deleteModel(t, s, "my-model")
	// No -m/--model in cmd is actually caught earlier (422 "cannot
	// determine model file path"), which is a fine, equally-safe outcome —
	// either way nothing gets deleted without a known, validated path.
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", w.Code, w.Body.String())
	}
}

func TestHandleAPIDeleteModel_ShardSiblingFallbackWithNoProvenance(t *testing.T) {
	s, modelsDir, _ := newRemovalTestServer(t)
	repoDir := filepath.Join(modelsDir, "org", "shardrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	shard1 := filepath.Join(repoDir, "model-00001-of-00002.gguf")
	shard2 := filepath.Join(repoDir, "model-00002-of-00002.gguf")
	for _, p := range []string{shard1, shard2} {
		if err := os.WriteFile(p, []byte("shard"), 0o644); err != nil {
			t.Fatalf("write shard: %v", err)
		}
	}
	s.cfg.Models = map[string]config.ModelConfig{
		"my-model": {Cmd: "llama-server -m " + shard1},
	}

	w, body := deleteModel(t, s, "my-model")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	for _, p := range []string{shard1, shard2} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("shard %s should be deleted (sibling discovery via GroupShards), stat err = %v", p, err)
		}
	}
	deletedFiles, ok := body["deleted_files"].([]any)
	if !ok || len(deletedFiles) != 2 {
		t.Fatalf("deleted_files = %v, want 2 entries (both shards)", body["deleted_files"])
	}
}

func TestHandleAPIDeleteModel_FullSetFromProvenanceIncludingAuxiliaries(t *testing.T) {
	s, modelsDir, weightsPath := newRemovalTestServer(t)
	tokenizerPath := filepath.Join(modelsDir, "org", "repo", "tokenizer.json")
	if err := os.WriteFile(tokenizerPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write tokenizer: %v", err)
	}

	now := time.Now()
	op := operation.NewFromPlan(operation.Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts: []operation.Artifact{
			{Path: "model.gguf", SizeBytes: 7, Role: operation.ArtifactRoleWeights},
			{Path: "tokenizer.json", SizeBytes: 2, Role: operation.ArtifactRoleTokenizer},
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

	w, body := deleteModel(t, s, "my-model")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	for _, p := range []string{weightsPath, tokenizerPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s should be deleted (from operation provenance), stat err = %v", p, err)
		}
	}
	deletedFiles, ok := body["deleted_files"].([]any)
	if !ok || len(deletedFiles) != 2 {
		t.Fatalf("deleted_files = %v, want 2 entries (weights + tokenizer)", body["deleted_files"])
	}
}

func TestHandleAPIDeleteModel_PartiallyMissingFilesStillSucceed(t *testing.T) {
	s, modelsDir, weightsPath := newRemovalTestServer(t)
	// A tokenizer path the operation claims but that never actually
	// exists on disk (e.g. removed manually beforehand).
	now := time.Now()
	op := operation.NewFromPlan(operation.Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts: []operation.Artifact{
			{Path: "model.gguf", SizeBytes: 7, Role: operation.ArtifactRoleWeights},
			{Path: "tokenizer.json", SizeBytes: 2, Role: operation.ArtifactRoleTokenizer},
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
	_ = modelsDir

	w, body := deleteModel(t, s, "my-model")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(weightsPath); !os.IsNotExist(err) {
		t.Fatalf("weights file should be deleted: %v", err)
	}
	missing, ok := body["missing_files"].([]any)
	if !ok || len(missing) != 1 {
		t.Fatalf("missing_files = %v, want 1 entry (the never-existed tokenizer.json)", body["missing_files"])
	}
}

func TestHandleAPIDeleteModel_ConfigFileUnsetStillDeletesFiles(t *testing.T) {
	s, _, weightsPath := newRemovalTestServer(t)
	s.configFile = ""

	w, body := deleteModel(t, s, "my-model")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(weightsPath); !os.IsNotExist(err) {
		t.Fatalf("weights file should still be deleted even without a config file: %v", err)
	}
	if body["config_removed"] != false {
		t.Fatalf(`body["config_removed"] = %v, want false`, body["config_removed"])
	}
}
