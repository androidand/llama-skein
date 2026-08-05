package operation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Registrar performs the config write + reload step once every required
// artifact for an operation is installed. Injected rather than implemented
// in this package because that belongs to internal/server
// (writeModelToConfig, triggerReload), which already imports this package —
// the dependency can't run the other way without a cycle. Receives the
// operation (for its Registration snapshot and ModelID) and the resolved
// on-disk path of the primary weights artifact.
type Registrar func(op *Operation, weightsPath string) error

// Executor drives one Operation through its execution phases: preflighting,
// resolving, downloading, verifying, installing, registering, reloading.
//
// This started as task 4.1's single-artifact vertical slice (migration plan
// step 3: "one unsharded GGUF vertical slice") and now also carries task
// 4.3's HTTP range resume (downloadOne/fetchArtifact), task 4.4's digest
// verification (verify/verifyDigest), and task 4.5's shard-set awareness
// (primaryWeightsPath picks the lowest-indexed shard, not just whichever
// one plan order listed first; download/verify/install already treated
// every artifact in the set as one all-or-nothing sequence since 4.1 — see
// download/verify/install's per-artifact loops, each returning on the
// first failure before Run ever reaches registering) — deliberately still
// not the full design.md decision 4 flow:
//   - no cooperative mid-flight cancellation (task 4.6): Run only stops
//     early if ctx itself is cancelled (server shutdown); a /cancel request
//     against the same operation ID does not interrupt an in-progress Run
//     started from a different Operation instance, because Run does not
//     reload the record from Store mid-flight to notice;
//   - no idempotent short-circuit for an already-complete artifact set at
//     the Run level (task 4.7) — downloadOne itself already skips
//     redownloading a ".part" file that's already at or past the declared
//     size (see its doc comment), but Run always executes preflight/verify/
//     install/register regardless of whether every artifact turns out to
//     already be complete;
//   - no automatic redispatch of Run() for an operation Recover() marked
//     interrupted after a process restart: nothing in this change's task
//     list adds a retry API or an auto-resume trigger (checked — there is no
//     "retry" endpoint anywhere in contracts/llama-skein.openapi.json), so
//     resumability here is a property Run()/downloadOne itself has
//     (exercised directly by executor_test.go), not yet something that
//     happens on its own.
//
// What it does provide: real download execution moved behind the state
// machine (the vertical slice task 4.1 asked for), atomic final rename
// (fsync during download, rename during install), HTTP range resume with
// restart fallback (task 4.3), SHA-256 digest verification when the plan
// provided one and a recorded warning when it didn't (task 4.4), correct
// shard-set handling (task 4.5), and a Run call that is not tied to any
// HTTP request's context — the caller (internal/server) passes a context
// whose lifetime is the server process, not the request that created the
// operation. That decoupling is what lets a downloaded partial file survive
// the client disconnecting, which is task 4.2's core requirement; task 4.2
// additionally wants partials retained across a later *retry*, and 4.3's
// resume logic is exactly what makes that retention matter in practice
// instead of just being an unused file sitting on disk.
type Executor struct {
	Store     *Store
	ModelsDir string
	Client    *http.Client // nil defaults to http.DefaultClient at Run time.

	// SafetyReserveBytes mirrors validateInstallPlan's disk-preflight
	// reserve (internal/server/apimodeloperations.go's
	// defaultDiskSafetyReserveBytes). Re-checked here, not just trusted from
	// plan-acceptance time, because real time has passed and other activity
	// on the host can have changed available disk space since then.
	SafetyReserveBytes int64

	// Register performs config write + reload once every artifact is
	// installed. A nil Register skips that step entirely (leaves the
	// operation at PhaseReloading→PhaseSucceeded with files installed on
	// disk but nothing registered) — used by tests that only care about the
	// download/verify/install path.
	Register Registrar

	// Now defaults to time.Now when nil; a seam for deterministic tests.
	Now func() time.Time

	// Logf receives progress/error lines; defaults to a no-op when nil so
	// tests don't need to supply one.
	Logf func(format string, args ...any)
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Executor) logf(format string, args ...any) {
	if e.Logf != nil {
		e.Logf(format, args...)
	}
}

func (e *Executor) client() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	return http.DefaultClient
}

// resolvedArtifact is one artifact's composed source URL and on-disk
// destination, parallel-indexed with Operation.Artifacts.
type resolvedArtifact struct {
	url  string
	dest string
}

// Run executes op from its current phase through to a terminal one
// (succeeded or failed), saving op to Store after every phase change and
// periodically during download. It returns only once op is terminal.
//
// Run assumes op is freshly created (PhaseQueued) — it is the caller's job
// (internal/server's create handler) to persist the queued record and hand
// this exactly one Operation instance to run to completion; Run does not
// itself guard against being called twice for the same operation ID.
func (e *Executor) Run(ctx context.Context, op *Operation) {
	if err := e.advance(op, PhasePreflighting); err != nil {
		e.terminate(op, err)
		return
	}
	if err := e.preflight(op); err != nil {
		e.terminate(op, err)
		return
	}

	if err := e.advance(op, PhaseResolving); err != nil {
		e.terminate(op, err)
		return
	}
	resolved, resolveErr := e.resolveArtifacts(op)
	if resolveErr != nil {
		e.terminate(op, resolveErr)
		return
	}

	if err := e.advance(op, PhaseDownloading); err != nil {
		e.terminate(op, err)
		return
	}
	if err := e.download(ctx, op, resolved); err != nil {
		e.terminate(op, err)
		return
	}

	if err := e.advance(op, PhaseVerifying); err != nil {
		e.terminate(op, err)
		return
	}
	if err := e.verify(op, resolved); err != nil {
		e.terminate(op, err)
		return
	}

	if err := e.advance(op, PhaseInstalling); err != nil {
		e.terminate(op, err)
		return
	}
	if err := e.install(resolved); err != nil {
		e.terminate(op, err)
		return
	}

	if err := e.advance(op, PhaseRegistering); err != nil {
		e.terminate(op, err)
		return
	}
	if e.Register != nil {
		weightsPath := primaryWeightsPath(op, resolved)
		if regErr := e.Register(op, weightsPath); regErr != nil {
			e.terminate(op, &Error{Code: ErrorInternal, Message: regErr.Error()})
			return
		}
	}

	// design.md decision 3 lists registering and reloading as distinct
	// phases; internal/server folds the actual work (config write + reload
	// trigger) into the one Register call above (see Registrar's doc
	// comment) — this transition exists so the persisted phase history
	// still shows the full sequence even though nothing new executes here.
	if err := e.advance(op, PhaseReloading); err != nil {
		e.terminate(op, err)
		return
	}

	if err := e.advance(op, PhaseSucceeded); err != nil {
		e.terminate(op, err)
		return
	}
}

// advance transitions op to phase and persists it. A transition failure
// here means Run called it in the wrong order — a bug in this file, not
// something a caller or a concurrent request did — but is still handled as
// data, not a panic, so a future real cause (like task 4.6's mid-flight
// cancellation reaching Run) fails the operation cleanly instead of
// crashing the process.
func (e *Executor) advance(op *Operation, phase Phase) *Error {
	if err := op.TransitionTo(phase, e.now()); err != nil {
		return &Error{Code: ErrorInternal, Message: err.Error()}
	}
	if err := e.Store.Save(op); err != nil {
		e.logf("operation %s: save after transition to %s: %v", op.ID, phase, err)
	}
	return nil
}

// terminate records opErr as op's terminal failure and persists it.
func (e *Executor) terminate(op *Operation, opErr *Error) {
	if opErr == nil {
		opErr = &Error{Code: ErrorInternal, Message: "operation: unknown execution error"}
	}
	if err := op.Fail(opErr.Code, opErr.Message, e.now()); err != nil {
		// op.Phase was already terminal (Fail()'s own TransitionTo refused)
		// — nothing to overwrite; whatever got there first is the real
		// outcome.
		e.logf("operation %s: could not record failure %s (%s): %v", op.ID, opErr.Code, opErr.Message, err)
		return
	}
	if err := e.Store.Save(op); err != nil {
		e.logf("operation %s: save failure: %v", op.ID, err)
	}
}

// preflight re-checks disk space (see SafetyReserveBytes's doc comment for
// why this is a fresh check, not a reuse of plan-acceptance time's result).
func (e *Executor) preflight(op *Operation) *Error {
	if e.ModelsDir == "" {
		return &Error{Code: ErrorInternal, Message: "operation: executor has no configured models directory"}
	}
	var remaining int64
	for _, a := range op.Artifacts {
		if a.BytesTotal != nil {
			remaining += *a.BytesTotal - a.BytesDownloaded
		}
	}
	if err := CheckDiskPreflight(e.ModelsDir, remaining, e.SafetyReserveBytes); err != nil {
		return &Error{Code: ErrorDiskInsufficient, Message: err.Error()}
	}
	return nil
}

// resolveArtifacts composes each artifact's source URL and destination path
// using the same safe-composition functions (task 3.1/3.2) validateInstallPlan
// already ran once at plan-acceptance time. Re-running them here is
// deliberate defense in depth, not redundant trust: it is cheap, and it
// means a change to ModelsDir between plan acceptance and execution is
// still caught rather than silently composing a stale or wrong path.
func (e *Executor) resolveArtifacts(op *Operation) ([]resolvedArtifact, *Error) {
	if e.ModelsDir == "" {
		return nil, &Error{Code: ErrorInternal, Message: "operation: executor has no configured models directory"}
	}
	resolved := make([]resolvedArtifact, len(op.Artifacts))
	for i, a := range op.Artifacts {
		url, err := ResolveArtifactURL(op.SourceRepository, op.SourceRevision, a.Path)
		if err != nil {
			return nil, &Error{Code: ErrorUntrustedSource, Message: err.Error()}
		}
		dest, err := ResolveArtifactDestination(e.ModelsDir, op.SourceRepository, a.Path)
		if err != nil {
			return nil, &Error{Code: ErrorUntrustedSource, Message: err.Error()}
		}
		resolved[i] = resolvedArtifact{url: url, dest: dest}
	}
	return resolved, nil
}

func (e *Executor) download(ctx context.Context, op *Operation, resolved []resolvedArtifact) *Error {
	for i, r := range resolved {
		if err := e.downloadOne(ctx, op, i, r); err != nil {
			return err
		}
	}
	return nil
}

// downloadOne fetches one artifact to its deterministic ".part" path (same
// suffix convention as the pre-existing internal/server/apipull.go path),
// updating and periodically persisting op.Artifacts[index].BytesDownloaded.
// Unlike apipull.go, an error here never removes the partial file — design.md
// decision 4 requires partials to survive interruption so a later resume can
// pick up where this left off; only an explicit cancellation cleanup policy
// (task 4.6) decides to remove one.
//
// Task 4.3: if a ".part" file already exists on disk when downloadOne
// starts — whichever way that happened, since there is no dedicated retry
// API in this contract (tasks.md's own task list never adds one; resumption
// is a property of downloadOne itself, exercised here directly and by
// executor_test.go, and left for a future task to trigger automatically on
// process-restart recovery) — its actual on-disk size is authoritative, not
// whatever op.Artifacts[index].BytesDownloaded last had persisted (that
// counter is only updated every saveEvery bytes and could undercount after
// a crash). downloadOne re-stats it, requests the remainder via HTTP Range,
// and falls back to a full restart with a recorded warning if the origin
// doesn't honor the range (design.md decision 4 points 1-3).
func (e *Executor) downloadOne(ctx context.Context, op *Operation, index int, r resolvedArtifact) *Error {
	if err := os.MkdirAll(filepath.Dir(r.dest), 0o755); err != nil {
		return &Error{Code: ErrorInternal, Message: fmt.Sprintf("create destination directory: %v", err)}
	}
	partial := r.dest + ".part"

	existingSize := int64(0)
	if fi, statErr := os.Stat(partial); statErr == nil {
		existingSize = fi.Size()
	} else if !os.IsNotExist(statErr) {
		return &Error{Code: ErrorInternal, Message: fmt.Sprintf("stat partial file: %v", statErr)}
	}
	if want := op.Artifacts[index].BytesTotal; want != nil && existingSize >= *want {
		// Already fully downloaded by an earlier attempt that crashed or
		// stopped before verify/install ran — nothing left to fetch.
		e.saveProgress(op, index, existingSize)
		return nil
	}

	resp, resuming, warning, fetchErr := e.fetchArtifact(ctx, op.Artifacts[index].Path, r.url, existingSize)
	if fetchErr != nil {
		return fetchErr
	}
	defer resp.Body.Close()
	if warning != "" {
		op.Warnings = append(op.Warnings, warning)
		existingSize = 0 // the origin refused the resume; write from the start regardless of what was already on disk.
	}

	openFlag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if resuming {
		openFlag = os.O_CREATE | os.O_APPEND | os.O_WRONLY
	}
	f, err := os.OpenFile(partial, openFlag, 0o644)
	if err != nil {
		return &Error{Code: ErrorInternal, Message: fmt.Sprintf("open partial file: %v", err)}
	}

	const saveEvery = 10 * 1024 * 1024
	buf := make([]byte, 256*1024)
	written := existingSize
	lastSaved := written
	for {
		if ctx.Err() != nil {
			f.Close()
			return &Error{Code: ErrorCancelled, Message: ctx.Err().Error()}
		}
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				f.Close()
				return &Error{Code: ErrorInternal, Message: fmt.Sprintf("write partial file: %v", writeErr)}
			}
			written += int64(n)
			if written-lastSaved >= saveEvery {
				e.saveProgress(op, index, written)
				lastSaved = written
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			f.Close()
			return &Error{Code: ErrorInternal, Message: fmt.Sprintf("read response body: %v", readErr)}
		}
	}

	// fsync before close, per design.md decision 4 point 6 ("fsyncs and
	// renames each artifact atomically") — the rename itself happens later,
	// in install, after verify has checked this synced file's size.
	if err := f.Sync(); err != nil {
		f.Close()
		return &Error{Code: ErrorInternal, Message: fmt.Sprintf("sync partial file: %v", err)}
	}
	if err := f.Close(); err != nil {
		return &Error{Code: ErrorInternal, Message: fmt.Sprintf("close partial file: %v", err)}
	}

	e.saveProgress(op, index, written)
	return nil
}

// fetchArtifact issues the GET for one artifact, requesting a resume range
// when existingSize > 0 and falling back to a full restart when the origin
// does not honor it (design.md decision 4 point 3). On success, the
// returned *http.Response's Body is the caller's to close.
//
// resuming reports whether the caller should append to the existing partial
// file (true) or truncate and start from zero (false — either because there
// was nothing to resume, or because the origin refused the range and a
// restart is required). warning is non-empty exactly in the refused-range
// case: the OpenAPI ModelOperationError "range_unsupported" doc comment
// ("...not that the operation failed outright — it only becomes a terminal
// error if the restart itself then fails") is why this is recorded as a
// warning and execution continues, rather than failing the operation.
func (e *Executor) fetchArtifact(ctx context.Context, artifactPath, url string, existingSize int64) (resp *http.Response, resuming bool, warning string, fetchErr *Error) {
	resp, fetchErr = e.doGet(ctx, url, existingSize)
	if fetchErr != nil {
		return nil, false, "", fetchErr
	}
	if existingSize == 0 {
		if resp.StatusCode != http.StatusOK {
			return nil, false, "", httpStatusError(resp, url, "download")
		}
		return resp, false, "", nil
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		return resp, true, "", nil
	case http.StatusOK, http.StatusRequestedRangeNotSatisfiable:
		resp.Body.Close()
		warning = fmt.Sprintf(
			"%s: origin did not honor a resume request (HTTP %d); restarting the download from the beginning",
			artifactPath, resp.StatusCode)
		resp, fetchErr = e.doGet(ctx, url, 0)
		if fetchErr != nil {
			return nil, false, "", fetchErr
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, "", httpStatusError(resp, url, "restart download")
		}
		return resp, false, warning, nil
	default:
		return nil, false, "", httpStatusError(resp, url, "download")
	}
}

// doGet issues one GET, with a "Range: bytes=from-" header when from > 0.
func (e *Executor) doGet(ctx context.Context, url string, from int64) (*http.Response, *Error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &Error{Code: ErrorInternal, Message: err.Error()}
	}
	if from > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", from))
	}
	resp, err := e.client().Do(req)
	if err != nil {
		return nil, &Error{Code: ErrorInternal, Message: fmt.Sprintf("download %s: %v", url, err)}
	}
	return resp, nil
}

// httpStatusError reads (and discards) a small error body and closes resp,
// so every non-2xx branch in fetchArtifact reports consistently.
func httpStatusError(resp *http.Response, url, verb string) *Error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	resp.Body.Close()
	return &Error{Code: ErrorInternal, Message: fmt.Sprintf("%s %s: HTTP %d: %s", verb, url, resp.StatusCode, string(body))}
}

func (e *Executor) saveProgress(op *Operation, index int, written int64) {
	op.Artifacts[index].BytesDownloaded = written
	op.UpdatedAt = e.now()
	if err := e.Store.Save(op); err != nil {
		e.logf("operation %s: save progress: %v", op.ID, err)
	}
}

// verify checks each downloaded ".part" file's actual size against its
// declared BytesTotal, then its content against its declared Digest when
// one was provided (task 4.4). ErrorDigestMismatch is reused for a size
// mismatch too because the OpenAPI error-code enum
// (contracts/llama-skein.openapi.json) has no separate "size mismatch"
// code — design.md decision 4 point 5 groups "declared size and digest"
// under one verification step, not two distinct failure reasons.
func (e *Executor) verify(op *Operation, resolved []resolvedArtifact) *Error {
	for i, r := range resolved {
		partial := r.dest + ".part"
		fi, err := os.Stat(partial)
		if err != nil {
			return &Error{Code: ErrorInternal, Message: fmt.Sprintf("stat partial file: %v", err)}
		}
		want := op.Artifacts[i].BytesTotal
		if want != nil && fi.Size() != *want {
			return &Error{Code: ErrorDigestMismatch, Message: fmt.Sprintf(
				"%s: downloaded %d bytes, expected %d", op.Artifacts[i].Path, fi.Size(), *want)}
		}

		digest := op.Artifacts[i].Digest
		if digest == nil {
			// InstallArtifact.digest's own doc comment (and design.md
			// decision 4 point 5): "a missing digest is reported as weaker
			// verification, not rejected outright" — size-only verification
			// is accepted, but the fact that it's weaker is recorded, not
			// silently assumed away.
			op.Warnings = append(op.Warnings, fmt.Sprintf(
				"%s: verified by size only; no digest was provided", op.Artifacts[i].Path))
			continue
		}
		if err := verifyDigest(partial, *digest); err != nil {
			return &Error{Code: ErrorDigestMismatch, Message: fmt.Sprintf("%s: %v", op.Artifacts[i].Path, err)}
		}
	}
	return nil
}

// verifyDigest hashes path's content and compares it against want, a
// "sha256:<hex>" string — the only form InstallArtifact.digest documents,
// already shape-checked at plan-acceptance time by validateInstallPlan's
// digestRe, but re-parsed defensively here rather than trusted blind.
// Streams the file through the hash instead of reading it into memory:
// artifacts here are GGUF weight files, often tens of gigabytes.
func verifyDigest(path, want string) error {
	hexWant, ok := strings.CutPrefix(want, "sha256:")
	if !ok {
		return fmt.Errorf("unsupported digest form %q (only \"sha256:<hex>\" is accepted)", want)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open for digest verification: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("read for digest verification: %w", err)
	}
	gotHex := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(gotHex, hexWant) {
		return fmt.Errorf("digest mismatch: got sha256:%s, want %s", gotHex, want)
	}
	return nil
}

// install atomically renames each artifact's already-synced ".part" file to
// its final destination.
func (e *Executor) install(resolved []resolvedArtifact) *Error {
	for _, r := range resolved {
		if err := os.Rename(r.dest+".part", r.dest); err != nil {
			return &Error{Code: ErrorInternal, Message: fmt.Sprintf("install %s: %v", r.dest, err)}
		}
	}
	return nil
}

// primaryWeightsPath returns the resolved destination of the weights
// artifact Registrar should point a backend command at. For a single,
// unsharded weights artifact there is only one choice. For a multi-shard
// set (task 4.5), llama.cpp's convention is to be pointed at shard 1 and
// auto-discover the rest via the "-NNNNN-of-MMMMM" filename convention it
// shares with ParseShardInfo (task 3.2) — so this picks the lowest-indexed
// shard among the weights-role artifacts, not just whichever one
// plan.artifacts happened to list first. A client is never required to
// submit shards in index order (task 3.3's validateWeightShardCompleteness
// only checks that a complete set is present, not that it's sorted), so
// trusting plan order here would have been a real correctness bug for any
// client that didn't happen to submit shards low-to-high.
func primaryWeightsPath(op *Operation, resolved []resolvedArtifact) string {
	firstWeights := -1
	shardBest := -1
	var shardBestIndex uint32
	for i, a := range op.Artifacts {
		if a.Role != ArtifactRoleWeights {
			continue
		}
		if firstWeights == -1 {
			firstWeights = i
		}
		if info, ok := ParseShardInfo(a.Path); ok {
			if shardBest == -1 || info.Index < shardBestIndex {
				shardBest = i
				shardBestIndex = info.Index
			}
		}
	}
	if shardBest != -1 {
		return resolved[shardBest].dest
	}
	if firstWeights != -1 {
		return resolved[firstWeights].dest
	}
	return ""
}
