package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/androidand/llama-skein/internal/thermal"
)

func TestServer_SilentMode_GetState_Unavailable(t *testing.T) {
	s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))
	s.silentMode = thermal.NewManager()

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/hardware/power", nil))

	if w.Code != http.StatusOK {
		t.Errorf("GET status = %d, want 200", w.Code)
	}

	var state thermal.State
	if err := json.NewDecoder(w.Body).Decode(&state); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if state.Available {
		t.Error("expected Available=false on non-AMD host")
	}
	if state.UnavailableReason == "" {
		t.Error("expected non-empty UnavailableReason when GPU unavailable")
	}
}

func TestServer_SilentMode_Put_503_Unavailable(t *testing.T) {
	s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))
	s.silentMode = thermal.NewManager()

	body := bytes.NewReader([]byte(`{"power_limit_pct":65,"temp_target_celsius":82}`))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/hardware/power", body))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("PUT status = %d, want 503", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := resp["error"]; !ok {
		t.Error("expected error field in 503 response")
	}
}

func TestServer_SilentMode_Delete_503_Unavailable(t *testing.T) {
	s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))
	s.silentMode = thermal.NewManager()

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/hardware/power", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("DELETE status = %d, want 503", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := resp["error"]; !ok {
		t.Error("expected error field in 503 response")
	}
}

func TestServer_SilentMode_Put_NoBody_503(t *testing.T) {
	s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))
	s.silentMode = thermal.NewManager()

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/hardware/power", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("PUT without body status = %d, want 503", w.Code)
	}
}

func TestServer_SilentMode_Put_EmptyBody_503(t *testing.T) {
	s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))
	s.silentMode = thermal.NewManager()

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/hardware/power", bytes.NewReader([]byte("{}"))))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("PUT empty body status = %d, want 503", w.Code)
	}
}


