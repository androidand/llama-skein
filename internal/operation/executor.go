package operation

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
// This is task 4.1's single-artifact vertical slice (migration plan step 3:
// "one unsharded GGUF vertical slice") — deliberately not the full design.md
// decision 4 flow yet:
//   - no HTTP range resume (task 4.3): a failed or restarted download always
//     starts the artifact over from byte 0;
//   - no digest verification (task 4.4): Artifact.Digest is carried on the
//     operation record but not read here, only the declared size is checked;
//   - no shard/auxiliary-set-aware install ordering (task 4.5): multiple
//     artifacts download and install fine, but nothing here groups shards or
//     treats a set specially;
//   - no cooperative mid-flight cancellation (task 4.6): Run only stops
//     early if ctx itself is cancelled (server shutdown); a /cancel request
//     against the same operation ID does not interrupt an in-progress Run
//     started from a different Operation instance, because Run does not
//     reload the record from Store mid-flight to notice;
//   - no idempotent short-circuit for an already-complete artifact set
//     (task 4.7): Run always redownloads.
//
// What it does provide: real download execution moved behind the state
// machine (the vertical slice this task asks for), atomic final rename
// (fsync during download, rename during install), and a Run call that is
// not tied to any HTTP request's context — the caller (internal/server)
// passes a context whose lifetime is the server process, not the request
// that created the operation. That decoupling is what lets a downloaded
// partial file survive the client disconnecting, which is task 4.2's core
// requirement; task 4.2 additionally wants partials retained across a
// later *retry*, which needs 4.3's resume logic to matter in practice.
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
// decision 4 requires partials to survive interruption so a later resume
// (task 4.3) can pick up where this left off; only an explicit cancellation
// cleanup policy (task 4.6) decides to remove one.
func (e *Executor) downloadOne(ctx context.Context, op *Operation, index int, r resolvedArtifact) *Error {
	if err := os.MkdirAll(filepath.Dir(r.dest), 0o755); err != nil {
		return &Error{Code: ErrorInternal, Message: fmt.Sprintf("create destination directory: %v", err)}
	}
	partial := r.dest + ".part"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return &Error{Code: ErrorInternal, Message: err.Error()}
	}
	resp, err := e.client().Do(req)
	if err != nil {
		return &Error{Code: ErrorInternal, Message: fmt.Sprintf("download %s: %v", r.url, err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &Error{Code: ErrorInternal, Message: fmt.Sprintf("download %s: HTTP %d: %s", r.url, resp.StatusCode, string(body))}
	}

	f, err := os.Create(partial)
	if err != nil {
		return &Error{Code: ErrorInternal, Message: fmt.Sprintf("create partial file: %v", err)}
	}

	const saveEvery = 10 * 1024 * 1024
	buf := make([]byte, 256*1024)
	var written int64
	lastSaved := int64(0)
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

func (e *Executor) saveProgress(op *Operation, index int, written int64) {
	op.Artifacts[index].BytesDownloaded = written
	op.UpdatedAt = e.now()
	if err := e.Store.Save(op); err != nil {
		e.logf("operation %s: save progress: %v", op.ID, err)
	}
}

// verify checks each downloaded ".part" file's actual size against its
// declared BytesTotal. Digest verification (task 4.4) is not implemented
// yet; ErrorDigestMismatch is reused for a size mismatch too because the
// OpenAPI error-code enum (contracts/llama-skein.openapi.json) has no
// separate "size mismatch" code — design.md decision 4 point 5 groups
// "declared size and digest" under one verification step, not two distinct
// failure reasons.
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

// primaryWeightsPath returns the resolved destination of op's weights
// artifact, for Registrar to build a run command against. For 4.1's
// single-unsharded-artifact scope there is exactly one; picking the right
// file to point a backend at for a multi-shard set (task 4.5) is not
// implemented here — this returns the first weights-role artifact found in
// plan order, which is only meaningful today because there is only ever one.
func primaryWeightsPath(op *Operation, resolved []resolvedArtifact) string {
	for i, a := range op.Artifacts {
		if a.Role == ArtifactRoleWeights {
			return resolved[i].dest
		}
	}
	return ""
}
