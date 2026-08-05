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
