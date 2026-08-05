package operation

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// rewriteHostRoundTripper redirects every request to target's scheme/host,
// keeping path/query intact — the standard trick for testing code that
// composes a fixed real-world URL (here, always https://huggingface.co/...,
// per ResolveArtifactURL) against a local httptest.Server instead.
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

func testClient(t *testing.T, serverURL string) *http.Client {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	return &http.Client{Transport: rewriteHostRoundTripper{target: u}}
}

// roundTripFunc adapts a function to http.RoundTripper, for tests that need
// to assert a request was never made at all.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testExecutorStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(t.TempDir(), 50)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func TestExecutor_Run_HappyPathSingleArtifact(t *testing.T) {
	content := []byte("fake gguf weights bytes, just enough to prove a real download happened")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/org/repo/resolve/deadbeef/model.gguf" {
			t.Errorf("unexpected request path: %s", r.URL.Path)
		}
		w.Write(content) //nolint:errcheck
	}))
	defer ts.Close()

	modelsDir := t.TempDir()
	store := testExecutorStore(t)

	plan := Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts: []Artifact{
			{Path: "model.gguf", SizeBytes: int64(len(content)), Role: ArtifactRoleWeights},
		},
		Registration: Registration{ModelID: "my-model", Backend: "llamacpp"},
	}
	op := NewFromPlan(plan, time.Now())
	if err := store.Save(op); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var registeredWeightsPath string
	var registeredModelID string
	exec := &Executor{
		Store:     store,
		ModelsDir: modelsDir,
		Client:    testClient(t, ts.URL),
		Register: func(op *Operation, weightsPath string) error {
			registeredWeightsPath = weightsPath
			registeredModelID = op.Registration.ModelID
			return nil
		},
	}
	exec.Run(context.Background(), op)

	if op.Phase != PhaseSucceeded {
		t.Fatalf("Phase = %s, want succeeded (error=%+v)", op.Phase, op.Error)
	}
	if op.Error != nil {
		t.Fatalf("Error = %+v, want nil on success", op.Error)
	}

	wantDest := filepath.Join(modelsDir, "org", "repo", "model.gguf")
	if registeredWeightsPath != wantDest {
		t.Fatalf("Register weightsPath = %q, want %q", registeredWeightsPath, wantDest)
	}
	if registeredModelID != "my-model" {
		t.Fatalf("Register saw ModelID = %q, want my-model", registeredModelID)
	}

	gotContent, err := os.ReadFile(wantDest)
	if err != nil {
		t.Fatalf("read installed file: %v", err)
	}
	if !bytes.Equal(gotContent, content) {
		t.Fatalf("installed file content = %q, want %q", gotContent, content)
	}
	if _, err := os.Stat(wantDest + ".part"); !os.IsNotExist(err) {
		t.Fatalf(".part file should be gone after a successful install, stat err = %v", err)
	}

	if op.Artifacts[0].BytesDownloaded != int64(len(content)) {
		t.Fatalf("BytesDownloaded = %d, want %d", op.Artifacts[0].BytesDownloaded, len(content))
	}

	loaded, err := store.Load(op.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Phase != PhaseSucceeded {
		t.Fatalf("persisted Phase = %s, want succeeded", loaded.Phase)
	}
}

func TestExecutor_Run_SkipsRegistrationWhenRegisterIsNil(t *testing.T) {
	content := []byte("weights")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content) //nolint:errcheck
	}))
	defer ts.Close()

	modelsDir := t.TempDir()
	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []Artifact{{Path: "model.gguf", SizeBytes: int64(len(content)), Role: ArtifactRoleWeights}},
		Registration:     Registration{ModelID: "my-model"},
	}, time.Now())
	store.Save(op) //nolint:errcheck

	exec := &Executor{Store: store, ModelsDir: modelsDir, Client: testClient(t, ts.URL)}
	exec.Run(context.Background(), op)

	if op.Phase != PhaseSucceeded {
		t.Fatalf("Phase = %s, want succeeded (error=%+v)", op.Phase, op.Error)
	}
}

func TestExecutor_Run_FailsOnHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer ts.Close()

	modelsDir := t.TempDir()
	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []Artifact{{Path: "model.gguf", SizeBytes: 1000, Role: ArtifactRoleWeights}},
		Registration:     Registration{ModelID: "my-model"},
	}, time.Now())
	store.Save(op) //nolint:errcheck

	exec := &Executor{Store: store, ModelsDir: modelsDir, Client: testClient(t, ts.URL)}
	exec.Run(context.Background(), op)

	if op.Phase != PhaseFailed {
		t.Fatalf("Phase = %s, want failed", op.Phase)
	}
	if op.Error == nil || op.Error.Code != ErrorInternal {
		t.Fatalf("Error = %+v, want ErrorInternal", op.Error)
	}
}

func TestExecutor_Run_FailsOnSizeMismatchAndRetainsThePartialFile(t *testing.T) {
	content := []byte("actual bytes on the wire")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content) //nolint:errcheck
	}))
	defer ts.Close()

	modelsDir := t.TempDir()
	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		// Declared size deliberately wrong — proves verify catches a
		// truncated-or-wrong download the way design.md decision 4 point 5
		// requires, independent of digest verification (task 4.4).
		Artifacts:    []Artifact{{Path: "model.gguf", SizeBytes: int64(len(content)) + 100, Role: ArtifactRoleWeights}},
		Registration: Registration{ModelID: "my-model"},
	}, time.Now())
	store.Save(op) //nolint:errcheck

	exec := &Executor{Store: store, ModelsDir: modelsDir, Client: testClient(t, ts.URL)}
	exec.Run(context.Background(), op)

	if op.Phase != PhaseFailed {
		t.Fatalf("Phase = %s, want failed", op.Phase)
	}
	if op.Error == nil || op.Error.Code != ErrorDigestMismatch {
		t.Fatalf("Error = %+v, want ErrorDigestMismatch", op.Error)
	}

	dest := filepath.Join(modelsDir, "org", "repo", "model.gguf")
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("final file should not exist when verify fails, stat err = %v", err)
	}
	partial, err := os.ReadFile(dest + ".part")
	if err != nil {
		t.Fatalf("partial file should be retained on verify failure (design.md decision 4): %v", err)
	}
	if !bytes.Equal(partial, content) {
		t.Fatalf("retained partial content = %q, want %q", partial, content)
	}
}

func TestExecutor_Run_FailsPreflightBeforeAnyNetworkCall(t *testing.T) {
	modelsDir := t.TempDir()
	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []Artifact{{Path: "model.gguf", SizeBytes: 1 << 62, Role: ArtifactRoleWeights}},
		Registration:     Registration{ModelID: "my-model"},
	}, time.Now())
	store.Save(op) //nolint:errcheck

	exec := &Executor{
		Store:     store,
		ModelsDir: modelsDir,
		Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			t.Fatal("no network request should be made when disk preflight already fails")
			return nil, errors.New("unreachable")
		})},
	}
	exec.Run(context.Background(), op)

	if op.Phase != PhaseFailed {
		t.Fatalf("Phase = %s, want failed", op.Phase)
	}
	if op.Error == nil || op.Error.Code != ErrorDiskInsufficient {
		t.Fatalf("Error = %+v, want ErrorDiskInsufficient", op.Error)
	}
}

func TestExecutor_Run_RetainsThePartialFileOnATruncatedDownload(t *testing.T) {
	// The server declares a Content-Length it never actually sends and
	// closes the connection early — the client's Read returns an error
	// mid-stream, the same shape as a real network interruption.
	partialContent := []byte("short")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.Write(partialContent) //nolint:errcheck
	}))
	defer ts.Close()

	modelsDir := t.TempDir()
	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []Artifact{{Path: "model.gguf", SizeBytes: 1000000, Role: ArtifactRoleWeights}},
		Registration:     Registration{ModelID: "my-model"},
	}, time.Now())
	store.Save(op) //nolint:errcheck

	exec := &Executor{Store: store, ModelsDir: modelsDir, Client: testClient(t, ts.URL)}
	exec.Run(context.Background(), op)

	if op.Phase != PhaseFailed {
		t.Fatalf("Phase = %s, want failed", op.Phase)
	}
	if op.Error == nil || op.Error.Code != ErrorInternal {
		t.Fatalf("Error = %+v, want ErrorInternal", op.Error)
	}

	dest := filepath.Join(modelsDir, "org", "repo", "model.gguf")
	partial, err := os.ReadFile(dest + ".part")
	if err != nil {
		t.Fatalf("partial file should be retained after a truncated download: %v", err)
	}
	if !bytes.Equal(partial, partialContent) {
		t.Fatalf("retained partial content = %q, want %q", partial, partialContent)
	}
}

func TestExecutor_Run_FailsWhenRegisterReturnsAnError(t *testing.T) {
	content := []byte("weights")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content) //nolint:errcheck
	}))
	defer ts.Close()

	modelsDir := t.TempDir()
	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []Artifact{{Path: "model.gguf", SizeBytes: int64(len(content)), Role: ArtifactRoleWeights}},
		Registration:     Registration{ModelID: "my-model"},
	}, time.Now())
	store.Save(op) //nolint:errcheck

	exec := &Executor{
		Store:     store,
		ModelsDir: modelsDir,
		Client:    testClient(t, ts.URL),
		Register: func(op *Operation, weightsPath string) error {
			return errors.New("config file is locked")
		},
	}
	exec.Run(context.Background(), op)

	if op.Phase != PhaseFailed {
		t.Fatalf("Phase = %s, want failed", op.Phase)
	}
	if op.Error == nil || op.Error.Code != ErrorInternal {
		t.Fatalf("Error = %+v, want ErrorInternal", op.Error)
	}

	// The artifact itself must still be installed — a registration failure
	// happens strictly after install (state machine order:
	// installing -> registering), so the file on disk is real even though
	// the model never got a config entry.
	dest := filepath.Join(modelsDir, "org", "repo", "model.gguf")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("artifact should still be installed despite the registration failure: %v", err)
	}
}
