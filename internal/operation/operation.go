// Package operation implements the host-local model-install operation state
// machine (openspec/changes/host-model-management-api, design.md decision 3).
//
// An Operation is created from a validated ModelInstallPlan and moves through
// a fixed sequence of phases until it reaches a terminal one. This package
// owns only the domain model and phase-transition rules; persistence (2.2),
// HTTP handlers (2.3), startup recovery (2.4), and secret redaction (2.5) are
// separate, later tasks.
package operation

import (
	"time"

	"github.com/google/uuid"
)

// Phase is one state in the operation state machine.
type Phase string

const (
	PhaseQueued       Phase = "queued"
	PhasePreflighting Phase = "preflighting"
	PhaseResolving    Phase = "resolving"
	PhaseDownloading  Phase = "downloading"
	PhaseVerifying    Phase = "verifying"
	PhaseInstalling   Phase = "installing"
	PhaseRegistering  Phase = "registering"
	PhaseReloading    Phase = "reloading"
	PhaseSucceeded    Phase = "succeeded"
	PhaseCancelled    Phase = "cancelled"
	PhaseFailed       Phase = "failed"
)

// Terminal reports whether p is one of the three phases an operation cannot
// leave: succeeded, cancelled, or failed.
func (p Phase) Terminal() bool {
	return p == PhaseSucceeded || p == PhaseCancelled || p == PhaseFailed
}

// Valid reports whether p is a known phase.
func (p Phase) Valid() bool {
	_, ok := happyPathIndex[p]
	return ok || p == PhaseCancelled || p == PhaseFailed
}

// ErrorCode is a stable, wire-visible reason for a terminal failure. Values
// match the OpenAPI ModelOperationError.code enum exactly (contracts/llama-skein.openapi.json).
type ErrorCode string

const (
	ErrorInvalidPlan      ErrorCode = "invalid_plan"
	ErrorUntrustedSource  ErrorCode = "untrusted_source"
	ErrorDiskInsufficient ErrorCode = "disk_insufficient"
	ErrorRangeUnsupported ErrorCode = "range_unsupported"
	ErrorDigestMismatch   ErrorCode = "digest_mismatch"
	ErrorShardIncomplete  ErrorCode = "shard_incomplete"
	ErrorReloadFailed     ErrorCode = "reload_failed"
	ErrorCancelled        ErrorCode = "cancelled"
	ErrorInternal         ErrorCode = "internal"
)

// Error is a typed terminal failure, mirroring apicontract.ModelOperationError.
type Error struct {
	Code    ErrorCode
	Message string
}

// ArtifactRole mirrors apicontract.ArtifactRole. Its own type rather than a
// reuse of the generated one, same reasoning as toAPIModelOperation's
// explicit conversion: this package stays independent of the wire types so
// it can be tested and evolved without an apicontract import.
type ArtifactRole string

const (
	ArtifactRoleWeights   ArtifactRole = "weights"
	ArtifactRoleProjector ArtifactRole = "projector"
	ArtifactRoleTokenizer ArtifactRole = "tokenizer"
	ArtifactRoleConfig    ArtifactRole = "config"
	ArtifactRoleOther     ArtifactRole = "other"
)

// ArtifactProgress tracks one artifact's download progress, mirroring
// apicontract.ModelOperationArtifactProgress plus the two fields execution
// needs that the wire response doesn't expose: Role (to pick the primary
// weights file and to know which artifacts validateWeightShardCompleteness-
// style shard checks apply to) and Digest (task 4.4's verification input).
type ArtifactProgress struct {
	Path            string
	BytesDownloaded int64
	// BytesTotal is nil when the artifact's expected size could not be
	// confirmed (weaker verification path) — see design.md decision 4.
	BytesTotal *int64
	Role       ArtifactRole
	Digest     *string
}

// Registration snapshots ModelInstallPlan.registration — the config-write
// target and parameters an operation applies once every required artifact
// is installed (design.md decision 2: the plan, including registration, is
// immutable once accepted, so it is captured on the operation record rather
// than re-read from the request that no longer exists by the time execution
// reaches the registering phase).
type Registration struct {
	ModelID     string
	DisplayName *string
	Backend     string
	Flags       []string
	TTL         *int
}

// Plan is the subset of an accepted ModelInstallPlan an Operation needs to
// execute — its own type rather than apicontract.ModelInstallPlan, same
// independence-from-wire-types reasoning as ArtifactRole above.
type Plan struct {
	SourceRepository string
	SourceRevision   string
	Artifacts        []Artifact
	Registration     Registration
}

// Artifact is one entry of Plan.Artifacts.
type Artifact struct {
	Path      string
	SizeBytes int64
	Digest    *string
	Role      ArtifactRole
}

// Operation is the host-local record for one model installation.
type Operation struct {
	ID    string
	Phase Phase

	// SourceRepository and SourceRevision snapshot the accepted plan's
	// identity (design.md decision 2: plans are immutable once accepted).
	SourceRepository string
	SourceRevision   string
	ModelID          string // registration target; also the resulting model ID once phase reaches succeeded.

	Artifacts []ArtifactProgress

	// Registration is the zero value for an Operation built with New()
	// directly (every pre-4.1 test does this and doesn't need it) and
	// populated for one built with NewFromPlan(), which execution (task 4.1+)
	// reads once it reaches PhaseRegistering.
	Registration Registration

	CreatedAt time.Time
	UpdatedAt time.Time

	Error    *Error
	Warnings []string
}

// New creates a queued Operation with a fresh host-generated ID. The caller
// has already validated the plan; New does not re-validate it.
func New(sourceRepository, sourceRevision, modelID string, artifacts []ArtifactProgress, now time.Time) *Operation {
	return &Operation{
		ID:               "op_" + uuid.New().String()[:12],
		Phase:            PhaseQueued,
		SourceRepository: sourceRepository,
		SourceRevision:   sourceRevision,
		ModelID:          modelID,
		Artifacts:        artifacts,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

// NewFromPlan is New(), plus capturing the artifact roles/digests and the
// registration target execution needs — the convenience constructor the
// real create handler uses. New() itself is untouched (still used directly
// by every pre-4.1 test that only cares about phase/store/recovery
// behavior and shouldn't need to grow a full Plan just to call it).
func NewFromPlan(plan Plan, now time.Time) *Operation {
	artifacts := make([]ArtifactProgress, len(plan.Artifacts))
	for i, a := range plan.Artifacts {
		total := a.SizeBytes
		artifacts[i] = ArtifactProgress{Path: a.Path, BytesTotal: &total, Role: a.Role, Digest: a.Digest}
	}
	op := New(plan.SourceRepository, plan.SourceRevision, plan.Registration.ModelID, artifacts, now)
	op.Registration = plan.Registration
	return op
}

// BytesDownloaded sums BytesDownloaded across every artifact.
func (o *Operation) BytesDownloaded() int64 {
	var total int64
	for _, artifact := range o.Artifacts {
		total += artifact.BytesDownloaded
	}
	return total
}

// BytesTotal sums BytesTotal across every artifact, or returns nil if any
// artifact's total is unknown — an aggregate is only meaningful when every
// part of it is.
func (o *Operation) BytesTotal() *int64 {
	var total int64
	for _, artifact := range o.Artifacts {
		if artifact.BytesTotal == nil {
			return nil
		}
		total += *artifact.BytesTotal
	}
	return &total
}
