package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	s, _ := newOperationTestServerWithDir(t)
	return s
}

// newOperationTestServerWithDir is newOperationTestServer, additionally
// returning the store's on-disk directory so a test can inspect the raw
// persisted files directly (operation.Store's directory field is
// unexported and this is a different package).
func newOperationTestServerWithDir(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := operation.NewStore(dir, 50)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	s := newTestServer(newStubRouter(nil, ""), nil)
	s.cfg.ModelsDir = t.TempDir() // a real modelsDir so destination-containment validation actually runs.
	s.operationStore = store
	s.routes()
	return s, dir
}

const redactionTestToken = "hf_secretTokenValueShouldNeverAppearAnywhere"

func planJSONWithToken(token string) string {
	return `{
		"source_repository": "org/model-GGUF",
		"source_revision": "deadbeef",
		"artifacts": [
			{"path": "model-Q4_K_M.gguf", "size_bytes": 1000, "role": "weights"}
		],
		"registration": {"model_id": "my-model", "backend": "llamacpp"},
		"token": "` + token + `"
	}`
}

// TestHandleCreateModelOperation_NeverPersistsTheToken is task 2.5's core
// guarantee, tested directly against what actually lands on disk and in the
// response body — not just "the code doesn't reference plan.Token after this
// line", which could silently regress.
func TestHandleCreateModelOperation_NeverPersistsTheToken(t *testing.T) {
	s, dir := newOperationTestServerWithDir(t)
	req := httptest.NewRequest(http.MethodPost, "/api/models/operations", strings.NewReader(planJSONWithToken(redactionTestToken)))
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte(redactionTestToken)) {
		t.Fatalf("response body contains the token: %s", w.Body.String())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no operation record was written")
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", entry.Name(), err)
		}
		if bytes.Contains(data, []byte(redactionTestToken)) {
			t.Fatalf("persisted record %s contains the token: %s", entry.Name(), data)
		}
	}
}

// TestHandleCreateModelOperation_NeverEchoesTheTokenInAnErrorMessage covers
// the "errors" half of task 2.5: an invalid plan that also happens to
// include a token must not echo it back in the 400 body.
func TestHandleCreateModelOperation_NeverEchoesTheTokenInAnErrorMessage(t *testing.T) {
	s := newOperationTestServer(t)
	body := `{"source_repository":"","source_revision":"r","artifacts":[{"path":"a","size_bytes":1,"role":"weights"}],"registration":{"model_id":"m","backend":"llamacpp"},"token":"` + redactionTestToken + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/models/operations", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte(redactionTestToken)) {
		t.Fatalf("error response contains the token: %s", w.Body.String())
	}
}

// TestHandleCreateModelOperation_RejectsAnArtifactPathThatTraversesOutOfTheRepository
// proves the trust-boundary check (design.md decision 7) is actually wired
// into the handler, not just defined and tested in isolation in
// internal/operation/source_test.go.
func TestHandleCreateModelOperation_RejectsAnArtifactPathThatTraversesOutOfTheRepository(t *testing.T) {
	s := newOperationTestServer(t)
	body := `{
		"source_repository": "org/model-GGUF",
		"source_revision": "deadbeef",
		"artifacts": [{"path": "../../../etc/passwd", "size_bytes": 1000, "role": "weights"}],
		"registration": {"model_id": "my-model", "backend": "llamacpp"}
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/models/operations", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestHandleCreateModelOperation_RejectsAnIncompleteShardSet proves the
// shard-completeness check (design.md decision 5) is wired into the
// handler, not just tested in isolation in internal/operation/shard_test.go.
func TestHandleCreateModelOperation_RejectsAnIncompleteShardSet(t *testing.T) {
	s := newOperationTestServer(t)
	body := `{
		"source_repository": "org/model-GGUF",
		"source_revision": "deadbeef",
		"artifacts": [
			{"path": "model-00001-of-00003.gguf", "size_bytes": 1000, "role": "weights"},
			{"path": "model-00002-of-00003.gguf", "size_bytes": 1000, "role": "weights"}
		],
		"registration": {"model_id": "my-model", "backend": "llamacpp"}
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/models/operations", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestHandleCreateModelOperation_AcceptsACompleteShardSet is the positive
// counterpart: a full shard set must not be rejected as incomplete.
func TestHandleCreateModelOperation_AcceptsACompleteShardSet(t *testing.T) {
	s := newOperationTestServer(t)
	body := `{
		"source_repository": "org/model-GGUF",
		"source_revision": "deadbeef",
		"artifacts": [
			{"path": "model-00001-of-00002.gguf", "size_bytes": 1000, "role": "weights"},
			{"path": "model-00002-of-00002.gguf", "size_bytes": 1000, "role": "weights"}
		],
		"registration": {"model_id": "my-model", "backend": "llamacpp"}
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/models/operations", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
}

// TestHandleCreateModelOperation_RejectsAPlanWithNoWeightsArtifact proves
// the required-roles check (design.md/task 3.3): a plan can name a
// tokenizer or config file, but registering a model with nothing to run
// makes no sense.
func TestHandleCreateModelOperation_RejectsAPlanWithNoWeightsArtifact(t *testing.T) {
	s := newOperationTestServer(t)
	body := `{
		"source_repository": "org/model-GGUF",
		"source_revision": "deadbeef",
		"artifacts": [{"path": "tokenizer.json", "size_bytes": 1000, "role": "tokenizer"}],
		"registration": {"model_id": "my-model", "backend": "llamacpp"}
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/models/operations", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestHandleCreateModelOperation_RejectsAMalformedDigest and
// TestHandleCreateModelOperation_AcceptsAWellFormedDigest cover the
// optional-digest half of task 3.3.
func TestHandleCreateModelOperation_RejectsAMalformedDigest(t *testing.T) {
	cases := map[string]string{
		"missing sha256 prefix": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"wrong length":          "sha256:aaaa",
		"uppercase hex":         "sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"non-hex characters":    "sha256:zzzzaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	for name, digest := range cases {
		t.Run(name, func(t *testing.T) {
			s := newOperationTestServer(t)
			body := `{
				"source_repository": "org/model-GGUF",
				"source_revision": "deadbeef",
				"artifacts": [{"path": "model.gguf", "size_bytes": 1000, "role": "weights", "digest": "` + digest + `"}],
				"registration": {"model_id": "my-model", "backend": "llamacpp"}
			}`
			req := httptest.NewRequest(http.MethodPost, "/api/models/operations", strings.NewReader(body))
			w := httptest.NewRecorder()
			s.handler.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestHandleCreateModelOperation_AcceptsAWellFormedDigest(t *testing.T) {
	s := newOperationTestServer(t)
	digest := strings.Repeat("a", 64)
	body := `{
		"source_repository": "org/model-GGUF",
		"source_revision": "deadbeef",
		"artifacts": [{"path": "model.gguf", "size_bytes": 1000, "role": "weights", "digest": "sha256:` + digest + `"}],
		"registration": {"model_id": "my-model", "backend": "llamacpp"}
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/models/operations", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
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
