package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/process"
)

func TestServer_InflightMiddleware(t *testing.T) {
	c := &inflightCounter{}
	mw := CreateInflightMiddleware(c)

	var duringRequest int64
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		duringRequest = c.Current()
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))

	if duringRequest != 1 {
		t.Errorf("counter during request = %d, want 1", duringRequest)
	}
	if got := c.Current(); got != 0 {
		t.Errorf("counter after request = %d, want 0", got)
	}
}

func TestServer_APIVersion(t *testing.T) {
	s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))
	s.build = BuildInfo{Version: "1.2.3", SkeinVersion: "1.2.3", Commit: "deadbeef", Date: "2026-05-19"}

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/system/version", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["version"] != "1.2.3" || got["commit"] != "deadbeef" || got["build_date"] != "2026-05-19" {
		t.Errorf("body = %v", got)
	}
}

func TestServer_APIMetrics_Empty(t *testing.T) {
	s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/system/metrics", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Errorf("body = %q, want []", body)
	}
}

func TestServer_APIPerformance_Unavailable(t *testing.T) {
	s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/hardware/performance", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestServer_APIEvents_InitialPayload(t *testing.T) {
	s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/system/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		s.ServeHTTP(w, req)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after context cancel")
	}

	body := w.Body.String()
	for _, want := range []string{`"type":"modelStatus"`, `"type":"inflight"`, `"type":"logData"`} {
		if !strings.Contains(body, want) {
			t.Errorf("initial SSE payload missing %s; body=%q", want, body)
		}
	}
}

// TestServer_HandleAPIUnloadModel_StillLoadedAfterUnloadIsA500 is task
// 5.2's defensive confirmation: Unload's own contract (internal/router/
// base.go) promises the process has already exited by the time it
// returns, so a caller still seeing it loaded afterward is a genuine
// anomaly, not a routine outcome — and must surface as a real error, not
// the unconditional "OK" this endpoint returned before this task no
// matter what actually happened.
func TestServer_HandleAPIUnloadModel_StillLoadedAfterUnloadIsA500(t *testing.T) {
	local := newStubRouter([]string{"m1"}, "")
	local.running = map[string]process.ProcessState{"m1": process.StateReady}
	s := newTestServer(local, newStubRouter(nil, ""))
	s.cfg = config.Config{Models: map[string]config.ModelConfig{"m1": {}}}

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/models/unload/m1", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
	if local.unloadCalls.Load() != 1 {
		t.Fatalf("unloadCalls = %d, want 1 (Unload must still have been called)", local.unloadCalls.Load())
	}
}

// TestServer_HandleAPIUnloadAll_ReportsStillRunningModels proves the same
// anomaly is observable (not silently dropped) on the bulk-unload path,
// via an additive field rather than a changed status code or a removed
// "msg":"ok" key.
func TestServer_HandleAPIUnloadAll_ReportsStillRunningModels(t *testing.T) {
	local := newStubRouter([]string{"m1"}, "")
	local.running = map[string]process.ProcessState{"m1": process.StateReady}
	s := newTestServer(local, newStubRouter(nil, ""))

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/models/unload", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["msg"] != "ok" {
		t.Fatalf(`body["msg"] = %v, want "ok" — the pre-existing success shape must not change`, body["msg"])
	}
	stillRunning, ok := body["still_running"].([]any)
	if !ok || len(stillRunning) != 1 || stillRunning[0] != "m1" {
		t.Fatalf(`body["still_running"] = %v, want ["m1"]`, body["still_running"])
	}
}

// TestServer_HandleAPIUnloadAll_NoStillRunningFieldOnTheNormalPath proves
// the additive field is absent (not present-but-empty) in the ordinary
// case, so nothing parsing this response gets a surprise new key when
// nothing went wrong.
func TestServer_HandleAPIUnloadAll_NoStillRunningFieldOnTheNormalPath(t *testing.T) {
	local := newStubRouter([]string{"m1"}, "")
	s := newTestServer(local, newStubRouter(nil, ""))

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/models/unload", nil))

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := body["still_running"]; present {
		t.Fatalf(`body["still_running"] = %v, want absent`, body["still_running"])
	}
}
