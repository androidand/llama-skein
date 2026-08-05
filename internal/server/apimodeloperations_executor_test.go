package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/operation"
	"github.com/androidand/llama-skein/pkg/apicontract"
)

// rewriteHostRoundTripper is executor_test.go's helper, duplicated here
// (unexported, package server) rather than shared — the two packages
// already don't share test helpers anywhere else in this codebase (see
// atomicWriteFile's precedent), and a handful of lines isn't worth an
// export.
type rewriteHostRoundTripper struct {
	target *url.URL
}

func (rt rewriteHostRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = rt.target.Scheme
	req.URL.Host = rt.target.Host
	req.Host = rt.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

// newExecutingOperationTestServer wires a Server that actually runs
// task 4.1's operation.Executor in the background when POST
// /api/models/operations succeeds — every other operations test
// (apimodeloperations_test.go) deliberately leaves runOperation nil so it
// never makes a real network call; this is the one place that proves the
// full wire-up (handler -> executor -> download -> config write -> reload)
// works end to end, pointed at a local httptest.Server standing in for
// huggingface.co.
func newExecutingOperationTestServer(t *testing.T, artifactContent []byte) *Server {
	t.Helper()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(artifactContent) //nolint:errcheck
	}))
	t.Cleanup(ts.Close)
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}

	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgFile, []byte("models: {}\n"), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	store, err := operation.NewStore(t.TempDir(), 50)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	s := newTestServerWithConfig(config.Config{}, newStubRouter(nil, ""), newStubRouter(nil, ""))
	s.configFile = cfgFile
	s.cfg.ModelsDir = t.TempDir()
	s.operationStore = store
	s.operationHTTPClient = &http.Client{Transport: rewriteHostRoundTripper{target: u}}
	s.reloadFn = func() {} // triggerReload runs this in its own goroutine; the test observes completion via the operation record's phase, not this.

	s.runOperation = func(ctx context.Context, op *operation.Operation) {
		s.newOperationExecutor().Run(ctx, op)
	}
	s.routes()
	return s
}

// waitForTerminalPhase polls the store for id until it reaches a terminal
// phase or timeout elapses. Run() executes on its own background goroutine
// (that is the entire point of task 4.1/4.2 — see Executor's doc comment),
// so a test observing its result has to poll the persisted record rather
// than assume any particular ordering against Run()'s internal steps.
func waitForTerminalPhase(t *testing.T, store *operation.Store, id string, timeout time.Duration) *operation.Operation {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		op, err := store.Load(id)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if op.Phase.Terminal() {
			return op
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for operation %s to reach a terminal phase; last seen phase = %s", id, op.Phase)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCreateModelOperation_EndToEnd_DownloadsInstallsRegistersAndReloads(t *testing.T) {
	content := []byte("a real GGUF file's bytes, or close enough for this test")
	s := newExecutingOperationTestServer(t, content)

	body := `{
		"source_repository": "org/repo-GGUF",
		"source_revision": "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		"artifacts": [{"path": "model.gguf", "size_bytes": ` + strconv.Itoa(len(content)) + `, "role": "weights"}],
		"registration": {"model_id": "e2e-model", "backend": "llamacpp"}
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/models/operations", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	var created apicontract.ModelOperation
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Phase != apicontract.ModelOperationPhaseQueued {
		t.Fatalf("created Phase = %s, want queued", created.Phase)
	}

	final := waitForTerminalPhase(t, s.operationStore, created.Id, 5*time.Second)
	if final.Phase != operation.PhaseSucceeded {
		t.Fatalf("final Phase = %s, want succeeded (error=%+v)", final.Phase, final.Error)
	}

	// registerInstalledModel writes config synchronously and only calls the
	// async s.triggerReload() (no-op in this test, see reloadFn above) after
	// that write has already returned — so by the time Run() has advanced
	// past registering to reach "succeeded" above, the config file write is
	// guaranteed complete; nothing here depends on reloadFn's own goroutine.

	installedPath := filepath.Join(s.cfg.ModelsDir, "org", "repo-GGUF", "model.gguf")
	gotContent, err := os.ReadFile(installedPath)
	if err != nil {
		t.Fatalf("read installed model file: %v", err)
	}
	if string(gotContent) != string(content) {
		t.Fatalf("installed content = %q, want %q", gotContent, content)
	}

	// The model must actually be in config.yaml now — the point of this
	// test is proving registerInstalledModel really wrote it, not just that
	// the callback was invoked.
	cfgBytes, err := os.ReadFile(s.configFile)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if !strings.Contains(string(cfgBytes), "e2e-model") {
		t.Fatalf("config file does not contain the registered model id:\n%s", cfgBytes)
	}
	if !strings.Contains(string(cfgBytes), installedPath) {
		t.Fatalf("config file's cmd does not reference the installed model path:\n%s", cfgBytes)
	}
}
