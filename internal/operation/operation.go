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

// ArtifactProgress tracks one artifact's download progress, mirroring
// apicontract.ModelOperationArtifactProgress.
type ArtifactProgress struct {
	Path            string
	BytesDownloaded int64
	// BytesTotal is nil when the artifact's expected size could not be
	// confirmed (weaker verification path) — see design.md decision 4.
	BytesTotal *int64
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
