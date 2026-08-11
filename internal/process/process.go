package process

import (
	"context"
	"net/http"
	"time"

	"github.com/androidand/llama-skein/internal/logmon"
)

type ProcessState string

const (
	StateStopped  ProcessState = ProcessState("stopped")
	StateStarting ProcessState = ProcessState("starting")
	StateReady    ProcessState = ProcessState("ready")
	StateStopping ProcessState = ProcessState("stopping")

	// process is shutdown and will not be restarted
	StateShutdown ProcessState = ProcessState("shutdown")

	// StateFailed means the last start or load attempt failed. It is distinct
	// from StateStopped, which means "not running, nothing went wrong" — a
	// caller cannot otherwise tell a broken model from an idle one, because
	// both used to report "stopped". Unlike Stopped/Shutdown this state is
	// reported by RunningModels, so the failure stays queryable after the
	// process is gone.
	StateFailed ProcessState = ProcessState("failed")
)

// StartableFrom reports whether a start may be attempted from state s.
// StateFailed counts: it means "idle, and the last attempt went wrong", which
// is still restartable. The crash-loop breaker, not this predicate, is what
// refuses a restart.
func StartableFrom(s ProcessState) bool {
	return s == StateStopped || s == StateFailed
}

// FailureCategory classifies why a start or load attempt failed, so callers can
// react without parsing a message.
type FailureCategory string

const (
	// FailureStart covers a backend that never reached a serving state.
	FailureStart FailureCategory = FailureCategory("start")
	// FailureCrash covers a backend that was ready and then exited on its own.
	FailureCrash FailureCategory = FailureCategory("crash")
)

// LoadError is the retained record of the most recent failure. Previously the
// error was handed to the one waiting caller and discarded, leaving nothing
// queryable behind.
type LoadError struct {
	Message  string          `json:"message"`
	Category FailureCategory `json:"category"`
	At       time.Time       `json:"at"`
	Attempts int             `json:"attempts"`
	// Class says WHY it failed (gpu-oom, host-oom, unsupported-arch, …),
	// where Category says when (start vs crash). Only a memory class is
	// eligible for an adaptive placement retry; everything else — including
	// the unclassified default — must never trigger one.
	Class FailureClass `json:"class,omitempty"`
}

type Process interface {
	// Run starts the process blocks until the process is terminated.
	// The timeout parameter controls how long to wait for the process to get
	// to a ready state to process traffic
	Run(timeout time.Duration) error

	// WaitReady blocks until the process is ready to serve requests
	// or the context is cancelled. It returns nil when the process is ready
	WaitReady(context.Context) error

	// Stop blocks until the process has terminated. It returns nil when
	// the process terminated as expected (exit 0)
	Stop(timeout time.Duration) error

	// State returns the current state of the process
	// Note: this is a snapshot of the state at the time of the call
	// and may change at any time after the call returns.
	State() ProcessState

	// LastError returns the most recent start/load failure, or nil if the
	// process has never failed. It is retained across a later successful
	// start so the failure stays auditable; State() is what tells you the
	// current condition.
	LastError() *LoadError

	// ServeHTTP forwards requests to the underlying process
	// Calling it when the process is not ready will result in a
	// 503 response with a body indicating it is a llama-swap-error
	ServeHTTP(http.ResponseWriter, *http.Request)

	// Logger returns the monitor that captures this process's stdout/stderr.
	Logger() *logmon.Monitor

	// SetCommandOverride replaces the launch command used by the NEXT start
	// ("" clears it). Adaptive placement retry uses this to relaunch a model
	// with a more conservative command after a memory failure, without
	// rebuilding the process or reloading the whole config.
	SetCommandOverride(cmd string, healthCheckSecs int)

	// CommandOverride returns the current override, or "" when none is set.
	CommandOverride() string

	// ResidentBytes reports how much memory the upstream process actually
	// has resident (0 when not running or unavailable). Host-level
	// "available memory" cannot measure a hybrid placement, because mmap'd
	// weights are reclaimable page cache; this counts them.
	ResidentBytes() int64
}
