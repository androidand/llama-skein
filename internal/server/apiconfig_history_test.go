package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/pkg/apicontract"
)

// newTestServerWithConfigFile is newTestServerWithConfig plus a real
// configFile on disk and a no-op reloadFn, for the config-history/reload/
// validate/rollback handlers that read/write the file and require reloadFn
// to be non-nil to proceed past the "reload not available" guard.
func newTestServerWithConfigFile(t *testing.T, cfg config.Config, configFile string) *Server {
	t.Helper()
	s := newTestServerWithConfig(cfg, newStubRouter(nil, ""), newStubRouter(nil, ""))
	s.configFile = configFile
	s.reloadFn = func() {}
	return s
}

func TestServer_ConfigReload_InvalidYAMLIsRejectedLoudly(t *testing.T) {
	// Regression for the 2026-07-30 incident: an invalid config reload
	// returned 202 {"status":"reloading"} while silently keeping the old
	// config active, with no way to discover the reload never happened.
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgFile, []byte("models: [this is not a valid map\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestServerWithConfigFile(t, config.Config{}, cfgFile)

	req := httptest.NewRequest(http.MethodPost, "/api/config/reload", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%q", w.Code, w.Body.String())
	}
	var resp apicontract.ReloadResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%q", err, w.Body.String())
	}
	if resp.Status == "reloading" {
		t.Error(`an invalid config must NOT report status "reloading"`)
	}
	if resp.Errors == nil || len(*resp.Errors) == 0 {
		t.Error("want a non-empty errors array describing the parse failure")
	}

	valid, errMsg, staleSince := s.runtimeStateOrDefault().Status()
	if valid {
		t.Error("runtime state should report the config invalid")
	}
	if errMsg == "" || staleSince == nil {
		t.Errorf("want error message and stale_since set, got err=%q staleSince=%v", errMsg, staleSince)
	}
}

func TestServer_ConfigReload_ValidConfigAccepted(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgFile, []byte("models: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var called atomic.Bool
	s := newTestServerWithConfigFile(t, config.Config{}, cfgFile)
	s.reloadFn = func() { called.Store(true) }

	req := httptest.NewRequest(http.MethodPost, "/api/config/reload", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%q", w.Code, w.Body.String())
	}
	var resp apicontract.ReloadResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "reloading" {
		t.Errorf("status = %q, want reloading", resp.Status)
	}
	// reloadFn runs in a goroutine; give it a moment.
	deadline := time.Now().Add(time.Second)
	for !called.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !called.Load() {
		t.Error("reloadFn was never invoked")
	}
}

func TestServer_ConfigReload_ServiceUnavailableWhenNoReloadFn(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgFile, []byte("models: {}\n"), 0o644)
	s := newTestServerWithConfig(config.Config{}, newStubRouter(nil, ""), newStubRouter(nil, ""))
	s.configFile = cfgFile
	// reloadFn intentionally left nil

	req := httptest.NewRequest(http.MethodPost, "/api/config/reload", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestServer_ConfigValidate_DryRunNeverWritesTheFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	original := "models: {}\n"
	if err := os.WriteFile(cfgFile, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestServerWithConfigFile(t, config.Config{}, cfgFile)

	body, _ := json.Marshal(apicontract.ConfigValidateRequest{Config: strPtr("models: [broken\n")})
	req := httptest.NewRequest(http.MethodPost, "/api/config/validate", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("validate always returns 200 (check the body), got %d", w.Code)
	}
	var resp apicontract.ConfigValidateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Valid {
		t.Error("want valid=false for broken YAML")
	}
	if resp.Errors == nil || len(*resp.Errors) == 0 {
		t.Error("want a non-empty errors array")
	}

	onDisk, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != original {
		t.Errorf("validate must never write to the config file; on-disk content changed to %q", onDisk)
	}
}

func TestServer_ConfigValidate_OnDiskFileWhenNoBody(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgFile, []byte("models: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestServerWithConfigFile(t, config.Config{}, cfgFile)

	req := httptest.NewRequest(http.MethodPost, "/api/config/validate", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	var resp apicontract.ConfigValidateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%q", err, w.Body.String())
	}
	if !resp.Valid {
		t.Errorf("on-disk config is valid, want valid=true, got errors=%v", resp.Errors)
	}
}

func TestServer_ConfigHistoryAndRollback_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	good := "healthCheckTimeout: 30\n"
	if err := os.WriteFile(cfgFile, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestServerWithConfigFile(t, config.Config{}, cfgFile)
	s.runtimeStateOrDefault().SetLastGood([]byte(good))

	// simulate an external bad replacement, then reload to pick it up and
	// (per the reload contract) snapshot the good state that preceded it.
	bad := "healthCheckTimeout: 45\n"
	if err := os.WriteFile(cfgFile, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	reloadReq := httptest.NewRequest(http.MethodPost, "/api/config/reload", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, reloadReq)
	if w.Code != http.StatusAccepted {
		t.Fatalf("reload of the (syntactically valid) bad config: status=%d body=%q", w.Code, w.Body.String())
	}
	// This test operates at the internal/server layer, below the real
	// reloadPass in package main that performs the actual snapshot — so
	// snapshot the pre-replacement state directly here to exercise history/
	// rollback in isolation.
	if err := config.SnapshotConfig(cfgFile, config.ConfigHistoryConfig{}, "test", "", []byte(good)); err != nil {
		t.Fatal(err)
	}

	histReq := httptest.NewRequest(http.MethodGet, "/api/config/history", nil)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, histReq)
	if w.Code != http.StatusOK {
		t.Fatalf("history status=%d body=%q", w.Code, w.Body.String())
	}
	var histResp apicontract.ConfigHistoryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &histResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(histResp.Entries) != 1 {
		t.Fatalf("want 1 history entry, got %d", len(histResp.Entries))
	}
	ref := histResp.Entries[0].Id

	rollbackBody, _ := json.Marshal(apicontract.ConfigRollbackRequest{Ref: ref})
	rbReq := httptest.NewRequest(http.MethodPost, "/api/config/rollback", bytes.NewReader(rollbackBody))
	w = httptest.NewRecorder()
	s.ServeHTTP(w, rbReq)
	if w.Code != http.StatusAccepted {
		t.Fatalf("rollback status=%d body=%q", w.Code, w.Body.String())
	}

	onDisk, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != good {
		t.Errorf("rollback did not restore the good config, got %q", onDisk)
	}
}

func TestServer_ConfigRollback_UnknownRefIs404(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgFile, []byte("models: {}\n"), 0o644)
	s := newTestServerWithConfigFile(t, config.Config{}, cfgFile)

	body, _ := json.Marshal(apicontract.ConfigRollbackRequest{Ref: "nope"})
	req := httptest.NewRequest(http.MethodPost, "/api/config/rollback", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func strPtr(s string) *string { return &s }
