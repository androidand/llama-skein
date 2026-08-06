package operation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

// seedPartialFile writes content to dest+".part" (creating dest's parent
// directories), simulating a prior, interrupted downloadOne attempt for
// task 4.3's resume tests.
func seedPartialFile(t *testing.T, dest string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(dest+".part", content, 0o644); err != nil {
		t.Fatalf("seed partial file: %v", err)
	}
}

func TestExecutor_Run_ResumesFromAnExistingPartialFileViaRangeRequest(t *testing.T) {
	full := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	already := full[:10]
	remainder := full[10:]

	var gotRange string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		if gotRange != "bytes=10-" {
			t.Errorf("Range header = %q, want %q", gotRange, "bytes=10-")
		}
		w.Header().Set("Content-Range", "bytes 10-36/37")
		w.WriteHeader(http.StatusPartialContent)
		w.Write(remainder) //nolint:errcheck
	}))
	defer ts.Close()

	modelsDir := t.TempDir()
	dest := filepath.Join(modelsDir, "org", "repo", "model.gguf")
	seedPartialFile(t, dest, already)

	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []Artifact{{Path: "model.gguf", SizeBytes: int64(len(full)), Role: ArtifactRoleWeights}},
		Registration:     Registration{ModelID: "my-model"},
	}, time.Now())
	store.Save(op) //nolint:errcheck

	exec := &Executor{Store: store, ModelsDir: modelsDir, Client: testClient(t, ts.URL)}
	exec.Run(context.Background(), op)

	if op.Phase != PhaseSucceeded {
		t.Fatalf("Phase = %s, want succeeded (error=%+v, warnings=%v)", op.Phase, op.Error, op.Warnings)
	}
	if gotRange == "" {
		t.Fatal("server was never actually hit with a Range request")
	}
	gotContent, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read installed file: %v", err)
	}
	if !bytes.Equal(gotContent, full) {
		t.Fatalf("installed content = %q, want %q", gotContent, full)
	}
	// One warning is expected here — task 4.4's "verified by size only; no
	// digest was provided" (this test's plan never sets Artifact.Digest) —
	// not a range_unsupported one: the resume itself succeeded cleanly.
	if len(op.Warnings) != 1 || !strings.Contains(op.Warnings[0], "verified by size only") {
		t.Fatalf("Warnings = %v, want exactly one size-only-verification warning and no range warning", op.Warnings)
	}
}

func TestExecutor_Run_RestartsWhenOriginIgnoresTheRangeRequest(t *testing.T) {
	full := []byte("the complete correct file content, sent in full")
	stalePartial := []byte("garbage-from-an-earlier-mismatched-attempt")

	requestCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		// Ignores any Range header entirely, as an origin without range
		// support would — always sends the whole file with 200.
		w.WriteHeader(http.StatusOK)
		w.Write(full) //nolint:errcheck
	}))
	defer ts.Close()

	modelsDir := t.TempDir()
	dest := filepath.Join(modelsDir, "org", "repo", "model.gguf")
	seedPartialFile(t, dest, stalePartial)

	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []Artifact{{Path: "model.gguf", SizeBytes: int64(len(full)), Role: ArtifactRoleWeights}},
		Registration:     Registration{ModelID: "my-model"},
	}, time.Now())
	store.Save(op) //nolint:errcheck

	exec := &Executor{Store: store, ModelsDir: modelsDir, Client: testClient(t, ts.URL)}
	exec.Run(context.Background(), op)

	if op.Phase != PhaseSucceeded {
		t.Fatalf("Phase = %s, want succeeded (error=%+v)", op.Phase, op.Error)
	}
	if requestCount != 2 {
		t.Fatalf("requestCount = %d, want 2 (the refused range request, then the restart)", requestCount)
	}
	gotContent, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read installed file: %v", err)
	}
	if !bytes.Equal(gotContent, full) {
		t.Fatalf("installed content = %q, want %q (stale partial bytes must not leak into the result)", gotContent, full)
	}
	// Two warnings: the range_unsupported-style restart warning, plus task
	// 4.4's size-only-verification warning (this test's plan never sets
	// Artifact.Digest either).
	if len(op.Warnings) != 2 {
		t.Fatalf("Warnings = %v, want exactly two (range restart + size-only verification)", op.Warnings)
	}
	if !strings.Contains(op.Warnings[0], "did not honor a resume request") {
		t.Fatalf("Warnings[0] = %q, want a range_unsupported-style message", op.Warnings[0])
	}
}

func TestExecutor_Run_RestartsOn416RangeNotSatisfiable(t *testing.T) {
	full := []byte("the complete correct file content, long enough that a shorter stale partial below it doesn't look already-complete")
	// Deliberately shorter than full — long enough to be a real,
	// non-degenerate resume attempt (existingSize > 0, so a Range header is
	// sent) but short enough that it isn't mistaken for an already-complete
	// download by downloadOne's "existingSize >= declared size" shortcut.
	stalePartial := []byte("stale-bytes")

	requestCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.Header.Get("Range") != "" {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(full) //nolint:errcheck
	}))
	defer ts.Close()

	modelsDir := t.TempDir()
	dest := filepath.Join(modelsDir, "org", "repo", "model.gguf")
	seedPartialFile(t, dest, stalePartial)

	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []Artifact{{Path: "model.gguf", SizeBytes: int64(len(full)), Role: ArtifactRoleWeights}},
		Registration:     Registration{ModelID: "my-model"},
	}, time.Now())
	store.Save(op) //nolint:errcheck

	exec := &Executor{Store: store, ModelsDir: modelsDir, Client: testClient(t, ts.URL)}
	exec.Run(context.Background(), op)

	if op.Phase != PhaseSucceeded {
		t.Fatalf("Phase = %s, want succeeded (error=%+v)", op.Phase, op.Error)
	}
	if requestCount != 2 {
		t.Fatalf("requestCount = %d, want 2 (the rejected range request, then the restart)", requestCount)
	}
	gotContent, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read installed file: %v", err)
	}
	if !bytes.Equal(gotContent, full) {
		t.Fatalf("installed content = %q, want %q", gotContent, full)
	}
	// Same two-warnings shape as the 200-ignores-range test above (range
	// restart + size-only verification).
	if len(op.Warnings) != 2 {
		t.Fatalf("Warnings = %v, want exactly two (range restart + size-only verification)", op.Warnings)
	}
}

func TestExecutor_Run_SkipsTheNetworkEntirelyWhenThePartialAlreadyMatchesTheDeclaredSize(t *testing.T) {
	full := []byte("already fully downloaded by an earlier attempt that crashed before install")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no network request should be made when the partial file already matches the declared size")
	}))
	defer ts.Close()

	modelsDir := t.TempDir()
	dest := filepath.Join(modelsDir, "org", "repo", "model.gguf")
	seedPartialFile(t, dest, full)

	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []Artifact{{Path: "model.gguf", SizeBytes: int64(len(full)), Role: ArtifactRoleWeights}},
		Registration:     Registration{ModelID: "my-model"},
	}, time.Now())
	store.Save(op) //nolint:errcheck

	exec := &Executor{Store: store, ModelsDir: modelsDir, Client: testClient(t, ts.URL)}
	exec.Run(context.Background(), op)

	if op.Phase != PhaseSucceeded {
		t.Fatalf("Phase = %s, want succeeded (error=%+v)", op.Phase, op.Error)
	}
	if op.Artifacts[0].BytesDownloaded != int64(len(full)) {
		t.Fatalf("BytesDownloaded = %d, want %d", op.Artifacts[0].BytesDownloaded, len(full))
	}
	gotContent, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read installed file: %v", err)
	}
	if !bytes.Equal(gotContent, full) {
		t.Fatalf("installed content = %q, want %q", gotContent, full)
	}
}

func sha256Digest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestExecutor_Run_AcceptsAMatchingDigestWithNoVerificationWarning(t *testing.T) {
	content := []byte("weights that match their declared digest exactly")
	digest := sha256Digest(content)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content) //nolint:errcheck
	}))
	defer ts.Close()

	modelsDir := t.TempDir()
	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts: []Artifact{
			{Path: "model.gguf", SizeBytes: int64(len(content)), Digest: &digest, Role: ArtifactRoleWeights},
		},
		Registration: Registration{ModelID: "my-model"},
	}, time.Now())
	store.Save(op) //nolint:errcheck

	exec := &Executor{Store: store, ModelsDir: modelsDir, Client: testClient(t, ts.URL)}
	exec.Run(context.Background(), op)

	if op.Phase != PhaseSucceeded {
		t.Fatalf("Phase = %s, want succeeded (error=%+v)", op.Phase, op.Error)
	}
	if len(op.Warnings) != 0 {
		t.Fatalf("Warnings = %v, want none — a matching digest was provided, so there is nothing weaker to report", op.Warnings)
	}
}

func TestExecutor_Run_FailsOnAMismatchedDigestAndRetainsThePartialFile(t *testing.T) {
	content := []byte("actual downloaded bytes")
	wrongDigest := sha256Digest([]byte("a completely different expected content"))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content) //nolint:errcheck
	}))
	defer ts.Close()

	modelsDir := t.TempDir()
	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts: []Artifact{
			{Path: "model.gguf", SizeBytes: int64(len(content)), Digest: &wrongDigest, Role: ArtifactRoleWeights},
		},
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
		t.Fatalf("final file should not exist when the digest fails to verify, stat err = %v", err)
	}
	partial, err := os.ReadFile(dest + ".part")
	if err != nil {
		t.Fatalf("partial file should be retained on digest-verify failure: %v", err)
	}
	if !bytes.Equal(partial, content) {
		t.Fatalf("retained partial content = %q, want %q", partial, content)
	}
}

func TestExecutor_Run_ReportsWeakerVerificationWhenNoDigestIsProvided(t *testing.T) {
	content := []byte("weights with no digest in the plan at all")
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
	if len(op.Warnings) != 1 || !strings.Contains(op.Warnings[0], "verified by size only") {
		t.Fatalf("Warnings = %v, want exactly one size-only-verification warning", op.Warnings)
	}
}

func TestVerifyDigest_RejectsAnUnsupportedDigestForm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model.gguf")
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	if err := verifyDigest(path, "md5:deadbeef"); err == nil {
		t.Fatal("verifyDigest() = nil, want an error for a non-sha256 digest form")
	}
}

// shardSetServer serves fixed content per artifact path, keyed by the
// path's basename — the multi-artifact tests below route several distinct
// artifacts (shards + an auxiliary file) through one httptest.Server this
// way, mirroring how a real repository serves every artifact from the same
// host.
func shardSetServer(t *testing.T, content map[string][]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		body, ok := content[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body) //nolint:errcheck
	}))
}

func TestExecutor_Run_MultiArtifactShardSetAndAuxiliary_HappyPath(t *testing.T) {
	shard1 := []byte("shard one content, the one that must be picked as primary")
	shard2 := []byte("shard two content")
	tokenizer := []byte("tokenizer.json content")

	ts := shardSetServer(t, map[string][]byte{
		"model-00001-of-00002.gguf": shard1,
		"model-00002-of-00002.gguf": shard2,
		"tokenizer.json":            tokenizer,
	})
	defer ts.Close()

	modelsDir := t.TempDir()
	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts: []Artifact{
			// Deliberately out of index order — task 4.5's whole point is
			// that primaryWeightsPath must not just trust plan order.
			{Path: "model-00002-of-00002.gguf", SizeBytes: int64(len(shard2)), Role: ArtifactRoleWeights},
			{Path: "model-00001-of-00002.gguf", SizeBytes: int64(len(shard1)), Role: ArtifactRoleWeights},
			{Path: "tokenizer.json", SizeBytes: int64(len(tokenizer)), Role: ArtifactRoleTokenizer},
		},
		Registration: Registration{ModelID: "my-model"},
	}, time.Now())
	store.Save(op) //nolint:errcheck

	var registeredWeightsPath string
	exec := &Executor{
		Store:     store,
		ModelsDir: modelsDir,
		Client:    testClient(t, ts.URL),
		Register: func(op *Operation, weightsPath string) error {
			registeredWeightsPath = weightsPath
			return nil
		},
	}
	exec.Run(context.Background(), op)

	if op.Phase != PhaseSucceeded {
		t.Fatalf("Phase = %s, want succeeded (error=%+v, warnings=%v)", op.Phase, op.Error, op.Warnings)
	}

	wantPrimary := filepath.Join(modelsDir, "org", "repo", "model-00001-of-00002.gguf")
	if registeredWeightsPath != wantPrimary {
		t.Fatalf("registered weightsPath = %q, want shard 1 (%q), not whichever shard plan order listed first", registeredWeightsPath, wantPrimary)
	}

	for name, want := range map[string][]byte{
		"model-00001-of-00002.gguf": shard1,
		"model-00002-of-00002.gguf": shard2,
		"tokenizer.json":            tokenizer,
	} {
		got, err := os.ReadFile(filepath.Join(modelsDir, "org", "repo", name))
		if err != nil {
			t.Fatalf("read installed %s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("installed %s content = %q, want %q", name, got, want)
		}
	}
}

func TestExecutor_Run_AbortsTheWholeSetAndNeverRegistersWhenOneArtifactFails(t *testing.T) {
	shard1 := []byte("shard one, downloads fine")
	// shard2 is intentionally absent from the server's content map, so it
	// 404s.

	ts := shardSetServer(t, map[string][]byte{
		"model-00001-of-00002.gguf": shard1,
	})
	defer ts.Close()

	modelsDir := t.TempDir()
	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts: []Artifact{
			{Path: "model-00001-of-00002.gguf", SizeBytes: int64(len(shard1)), Role: ArtifactRoleWeights},
			{Path: "model-00002-of-00002.gguf", SizeBytes: 1000, Role: ArtifactRoleWeights},
		},
		Registration: Registration{ModelID: "my-model"},
	}, time.Now())
	store.Save(op) //nolint:errcheck

	registerCalled := false
	exec := &Executor{
		Store:     store,
		ModelsDir: modelsDir,
		Client:    testClient(t, ts.URL),
		Register: func(op *Operation, weightsPath string) error {
			registerCalled = true
			return nil
		},
	}
	exec.Run(context.Background(), op)

	if op.Phase != PhaseFailed {
		t.Fatalf("Phase = %s, want failed", op.Phase)
	}
	if registerCalled {
		t.Fatal("Register must never be called when any artifact in the set fails — a shard set is one operation, not independent per-file successes")
	}

	// The shard that succeeded is left on disk (as a retained partial or
	// installed file, per design.md decision 4's partial-retention
	// guarantee) — the point here is only that the model was never
	// registered with an incomplete weight set, not that nothing was
	// downloaded at all.
	if _, err := os.Stat(filepath.Join(modelsDir, "org", "repo", "model-00001-of-00002.gguf") + ".part"); err != nil {
		t.Fatalf("shard 1's partial should still be on disk: %v", err)
	}
}

// TestExecutor_Run_StopsWhenAConcurrentCancelRequestTransitionsTheStore is
// task 4.6's core proof: a /cancel request is a separate *Operation
// instance loaded and saved by internal/server's
// handleAPICancelModelOperation, not a field flipped on Run's own op. This
// reproduces that exact shape — a second Load/Cancel/Save sequence against
// the same store, concurrently with Run — and checks Run neither ignores
// it nor clobbers the cancelled record with its own stale progress.
func TestExecutor_Run_StopsWhenAConcurrentCancelRequestTransitionsTheStore(t *testing.T) {
	firstChunkSent := make(chan struct{})
	proceedWithRest := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test server's ResponseWriter does not support flushing")
		}
		w.Write(bytes.Repeat([]byte("a"), 1024)) //nolint:errcheck
		flusher.Flush()
		close(firstChunkSent)
		// A real sleep, not just <-proceedWithRest: HTTP/TCP buffering means
		// "the server flushed chunk 1" does not imply "the client's Read()
		// has already returned with just chunk 1" — if this handler raced
		// straight to sending chunk 2 as soon as the (very fast) test-side
		// cancel sequence closes proceedWithRest, both chunks could already
		// be sitting in the client's read buffer before downloadOne's loop
		// ever calls Read() for the first time, collapsing this into one
		// read that bypasses the whole point of the test. This sleep gives
		// the client's first Read() plenty of real wall-clock time to
		// return with only chunk 1, so the second chunk's eventual arrival
		// triggers a genuinely separate Read() — and by construction (see
		// below) the test's cancellation is always saved well within this
		// window, before <-proceedWithRest can unblock.
		time.Sleep(50 * time.Millisecond)
		<-proceedWithRest
		w.Write(bytes.Repeat([]byte("b"), 1024)) //nolint:errcheck
	}))
	defer ts.Close()

	modelsDir := t.TempDir()
	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []Artifact{{Path: "model.gguf", SizeBytes: 2048, Role: ArtifactRoleWeights}},
		Registration:     Registration{ModelID: "my-model"},
	}, time.Now())
	store.Save(op) //nolint:errcheck

	exec := &Executor{
		Store:     store,
		ModelsDir: modelsDir,
		Client:    testClient(t, ts.URL),
		// Small enough that the read loop re-checks cancellation well
		// before this test's 2048-byte artifact finishes, without the test
		// needing to transfer tens of megabytes to exercise it.
		ProgressSaveIntervalBytes: 512,
	}

	done := make(chan struct{})
	go func() {
		exec.Run(context.Background(), op)
		close(done)
	}()

	<-firstChunkSent
	// The real /cancel handler's exact shape: load a separate instance,
	// transition it, save it — Run must notice this through the store, not
	// through op (Run's own instance is never touched here).
	cancelling, err := store.Load(op.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cancelling.Phase.Terminal() {
		t.Fatalf("loaded phase %s is already terminal; the test's timing assumption (cancel while still downloading) broke", cancelling.Phase)
	}
	if err := cancelling.Cancel(time.Now()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := store.Save(cancelling); err != nil {
		t.Fatalf("Save: %v", err)
	}
	close(proceedWithRest)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after a concurrent cancellation")
	}

	final, err := store.Load(op.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if final.Phase != PhaseCancelled {
		t.Fatalf("final Phase = %s, want cancelled — Run must not overwrite the cancelled record with its own stale progress", final.Phase)
	}
}

// TestExecutor_Run_SkipsTheNetworkWhenTheFinalDestinationAlreadyMatches is
// task 4.7's core proof: resubmitting the same install plan (a fresh
// Operation targeting a destination an earlier, already-succeeded
// operation fully installed) must not redownload the artifact at all.
func TestExecutor_Run_SkipsTheNetworkWhenTheFinalDestinationAlreadyMatches(t *testing.T) {
	content := []byte("already installed by an earlier successful operation")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no network request should be made when the final destination already matches")
	}))
	defer ts.Close()

	modelsDir := t.TempDir()
	dest := filepath.Join(modelsDir, "org", "repo", "model.gguf")
	seedFile(t, dest, content) // the FINAL file, not a ".part" one.

	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []Artifact{{Path: "model.gguf", SizeBytes: int64(len(content)), Role: ArtifactRoleWeights}},
		Registration:     Registration{ModelID: "my-model"},
	}, time.Now())
	store.Save(op) //nolint:errcheck

	var registeredWeightsPath string
	exec := &Executor{
		Store:     store,
		ModelsDir: modelsDir,
		Client:    testClient(t, ts.URL),
		Register: func(op *Operation, weightsPath string) error {
			registeredWeightsPath = weightsPath
			return nil
		},
	}
	exec.Run(context.Background(), op)

	if op.Phase != PhaseSucceeded {
		t.Fatalf("Phase = %s, want succeeded (error=%+v)", op.Phase, op.Error)
	}
	if registeredWeightsPath != dest {
		t.Fatalf("registered weightsPath = %q, want %q", registeredWeightsPath, dest)
	}
	if op.Artifacts[0].BytesDownloaded != int64(len(content)) {
		t.Fatalf("BytesDownloaded = %d, want %d (the already-installed file's size)", op.Artifacts[0].BytesDownloaded, len(content))
	}
	gotContent, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if !bytes.Equal(gotContent, content) {
		t.Fatalf("final file content changed unexpectedly: got %q, want %q", gotContent, content)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatalf("no .part file should ever have been created, stat err = %v", err)
	}
}

// TestExecutor_Run_RedownloadsWhenTheFinalDestinationSizeDoesNotMatch
// proves the shortcut only fires on a genuine size match — a same-name
// file with the wrong size (e.g. a stale or unrelated file already at that
// path) is not mistaken for a completed install.
func TestExecutor_Run_RedownloadsWhenTheFinalDestinationSizeDoesNotMatch(t *testing.T) {
	correct := []byte("the correct, freshly downloaded content")
	stale := []byte("wrong size content")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(correct) //nolint:errcheck
	}))
	defer ts.Close()

	modelsDir := t.TempDir()
	dest := filepath.Join(modelsDir, "org", "repo", "model.gguf")
	seedFile(t, dest, stale)

	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts:        []Artifact{{Path: "model.gguf", SizeBytes: int64(len(correct)), Role: ArtifactRoleWeights}},
		Registration:     Registration{ModelID: "my-model"},
	}, time.Now())
	store.Save(op) //nolint:errcheck

	exec := &Executor{Store: store, ModelsDir: modelsDir, Client: testClient(t, ts.URL)}
	exec.Run(context.Background(), op)

	if op.Phase != PhaseSucceeded {
		t.Fatalf("Phase = %s, want succeeded (error=%+v)", op.Phase, op.Error)
	}
	gotContent, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if !bytes.Equal(gotContent, correct) {
		t.Fatalf("final file content = %q, want the freshly downloaded %q, not the stale wrong-size original", gotContent, correct)
	}
}

// TestExecutor_Run_FailsVerifyWhenAnAlreadyPresentFileHasTheRightSizeButWrongDigest
// proves downloadOne's size-only shortcut is not the last word: a
// same-size-but-corrupt existing file is still caught by verify running
// against that same final file, not silently accepted just because the
// network was skipped.
func TestExecutor_Run_FailsVerifyWhenAnAlreadyPresentFileHasTheRightSizeButWrongDigest(t *testing.T) {
	// Built from the same base length rather than two hand-typed strings —
	// an earlier draft of this test hand-counted characters in two
	// separate string literals and got the lengths wrong, which the
	// deliberate self-check further down caught before this landed.
	correctContent := []byte("correct content used only to derive the expected digest")
	wrongButRightSized := bytes.Repeat([]byte("x"), len(correctContent))
	rightDigest := sha256Digest(correctContent)
	if len(wrongButRightSized) != len(correctContent) {
		t.Fatalf("test fixture bug: lengths must match (%d vs %d)", len(wrongButRightSized), len(correctContent))
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no network request should be made — the shortcut fires on size alone; this test is specifically about what happens after that")
	}))
	defer ts.Close()

	modelsDir := t.TempDir()
	dest := filepath.Join(modelsDir, "org", "repo", "model.gguf")
	seedFile(t, dest, wrongButRightSized)

	store := testExecutorStore(t)
	op := NewFromPlan(Plan{
		SourceRepository: "org/repo",
		SourceRevision:   "deadbeef",
		Artifacts: []Artifact{
			{Path: "model.gguf", SizeBytes: int64(len(wrongButRightSized)), Digest: &rightDigest, Role: ArtifactRoleWeights},
		},
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
}
