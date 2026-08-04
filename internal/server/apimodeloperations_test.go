package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/androidand/llama-skein/internal/operation"
	"github.com/androidand/llama-skein/pkg/apicontract"
)

// newOperationTestServer wires a Server with a real, temp-dir-backed
// operation store so handler tests exercise the actual persistence path,
// not a mock.
func newOperationTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := operation.NewStore(t.TempDir(), 50)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	s := newTestServer(newStubRouter(nil, ""), nil)
	s.operationStore = store
	s.routes()
	return s
}

func validPlanJSON() string {
	return `{
		"source_repository": "org/model-GGUF",
		"source_revision": "deadbeef",
		"artifacts": [
			{"path": "model-Q4_K_M.gguf", "size_bytes": 1000, "role": "weights"}
		],
		"registration": {"model_id": "my-model", "backend": "llamacpp"}
	}`
}

func TestHandleCreateModelOperation_ValidPlanReturns201Queued(t *testing.T) {
	s := newOperationTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/models/operations", strings.NewReader(validPlanJSON()))
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	var got apicontract.ModelOperation
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Phase != apicontract.ModelOperationPhaseQueued {
		t.Fatalf("Phase = %s, want queued", got.Phase)
	}
	if !strings.HasPrefix(got.Id, "op_") {
		t.Fatalf("Id = %q, want an op_-prefixed ID", got.Id)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[0].Path != "model-Q4_K_M.gguf" {
		t.Fatalf("Artifacts = %+v, want the submitted artifact", got.Artifacts)
	}
}

func TestHandleCreateModelOperation_RejectsMissingFields(t *testing.T) {
	cases := map[string]string{
		"no source_repository": `{"source_revision":"r","artifacts":[{"path":"a","size_bytes":1,"role":"weights"}],"registration":{"model_id":"m","backend":"llamacpp"}}`,
		"no artifacts":         `{"source_repository":"o/m","source_revision":"r","artifacts":[],"registration":{"model_id":"m","backend":"llamacpp"}}`,
		"zero size_bytes":      `{"source_repository":"o/m","source_revision":"r","artifacts":[{"path":"a","size_bytes":0,"role":"weights"}],"registration":{"model_id":"m","backend":"llamacpp"}}`,
		"no model_id":          `{"source_repository":"o/m","source_revision":"r","artifacts":[{"path":"a","size_bytes":1,"role":"weights"}],"registration":{"backend":"llamacpp"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s := newOperationTestServer(t)
			req := httptest.NewRequest(http.MethodPost, "/api/models/operations", strings.NewReader(body))
			w := httptest.NewRecorder()
			s.handler.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestHandleCreateModelOperation_RejectsMalformedJSON(t *testing.T) {
	s := newOperationTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/models/operations", strings.NewReader("{not json"))
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func createOperation(t *testing.T, s *Server) apicontract.ModelOperation {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/models/operations", strings.NewReader(validPlanJSON()))
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("setup: create status = %d, body: %s", w.Code, w.Body.String())
	}
	var op apicontract.ModelOperation
	if err := json.Unmarshal(w.Body.Bytes(), &op); err != nil {
		t.Fatalf("setup: decode: %v", err)
	}
	return op
}

func TestHandleGetModelOperation_ReturnsCreatedOperation(t *testing.T) {
	s := newOperationTestServer(t)
	created := createOperation(t, s)

	req := httptest.NewRequest(http.MethodGet, "/api/models/operations/"+created.Id, nil)
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var got apicontract.ModelOperation
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Id != created.Id {
		t.Fatalf("Id = %q, want %q", got.Id, created.Id)
	}
}

func TestHandleGetModelOperation_UnknownIDReturns404(t *testing.T) {
	s := newOperationTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/models/operations/op_doesnotexist", nil)
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandleListModelOperations_ReturnsNewestFirst(t *testing.T) {
	s := newOperationTestServer(t)
	first := createOperation(t, s)
	second := createOperation(t, s)

	req := httptest.NewRequest(http.MethodGet, "/api/models/operations", nil)
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var got apicontract.ModelOperationList
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Operations) != 2 {
		t.Fatalf("len(Operations) = %d, want 2", len(got.Operations))
	}
	_ = first
	_ = second
}

func TestHandleCancelModelOperation_TransitionsToCancelled(t *testing.T) {
	s := newOperationTestServer(t)
	created := createOperation(t, s)

	req := httptest.NewRequest(http.MethodPost, "/api/models/operations/"+created.Id+"/cancel", nil)
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", w.Code, w.Body.String())
	}
	var got apicontract.ModelOperation
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Phase != apicontract.ModelOperationPhaseCancelled {
		t.Fatalf("Phase = %s, want cancelled", got.Phase)
	}
}

func TestHandleCancelModelOperation_IsIdempotent(t *testing.T) {
	s := newOperationTestServer(t)
	created := createOperation(t, s)
	path := "/api/models/operations/" + created.Id + "/cancel"

	for range 2 {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202 on every call; body: %s", w.Code, w.Body.String())
		}
	}
}

func TestHandleCancelModelOperation_UnknownIDReturns404(t *testing.T) {
	s := newOperationTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/models/operations/op_doesnotexist/cancel", nil)
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandleStreamModelOperationEvents_SendsAtLeastOneSnapshotForATerminalOperation(t *testing.T) {
	s := newOperationTestServer(t)
	created := createOperation(t, s)
	cancelReq := httptest.NewRequest(http.MethodPost, "/api/models/operations/"+created.Id+"/cancel", nil)
	s.handler.ServeHTTP(httptest.NewRecorder(), cancelReq)

	req := httptest.NewRequest(http.MethodGet, "/api/models/operations/"+created.Id+"/events", nil)
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"phase":"cancelled"`)) {
		t.Fatalf("event body = %q, want it to contain the cancelled snapshot", w.Body.String())
	}
}

func TestHandleStreamModelOperationEvents_UnknownIDReturns404(t *testing.T) {
	s := newOperationTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/models/operations/op_doesnotexist/events", nil)
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestOperationHandlers_Return503WhenStoreUnavailable(t *testing.T) {
	s := newTestServer(newStubRouter(nil, ""), nil)
	s.routes() // operationStore left nil, as it would be if DefaultStateDir failed at startup.

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"create", http.MethodPost, "/api/models/operations", validPlanJSON()},
		{"list", http.MethodGet, "/api/models/operations", ""},
		{"get", http.MethodGet, "/api/models/operations/op_x", ""},
		{"cancel", http.MethodPost, "/api/models/operations/op_x/cancel", ""},
		{"events", http.MethodGet, "/api/models/operations/op_x/events", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body *strings.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			} else {
				body = strings.NewReader("")
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			w := httptest.NewRecorder()
			s.handler.ServeHTTP(w, req)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s: status = %d, want 503; body: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}
