// Package botworkspace owns the declarative lifecycle of a bot's workspace.
//
// The API layer records intent (present / absent) and the reconciler in this
// package keeps the observed state converging toward it: provisioning,
// retrying with backoff, tearing down, and deriving bots.status. Every backend
// operation is idempotent and every write of an observation happens on a
// fresh context, so a lost request context or a crashed Server never strands
// a bot in a transitional status.
package botworkspace

import (
	"errors"
	"time"

	ctr "github.com/felinics/memoh/internal/container"
)

// Desired states.
const (
	DesiredPresent = "present"
	DesiredAbsent  = "absent"
)

// Observed states.
const (
	ObservedAbsent       = "absent"
	ObservedProvisioning = "provisioning"
	ObservedRunning      = "running"
	ObservedStopped      = "stopped"
	ObservedFailed       = "failed"
	ObservedRemoving     = "removing"
)

// Failure phases recorded in last_error_phase.
const (
	PhaseImagePrepare = "image_prepare"
	PhaseStart        = "start"
	PhaseBridge       = "bridge"
	PhaseBootstrap    = "bootstrap"
	PhaseTeardown     = "teardown"
)

// Bot statuses derived from the workspace state. They mirror the values in
// internal/bots without importing it (bots depends on this package through an
// interface, so the dependency must not point back).
const (
	BotStatusCreating = "creating"
	BotStatusReady    = "ready"
	BotStatusFailed   = "failed"
)

// Workspace is one row of bot_workspaces.
type Workspace struct {
	BotID              string
	TeamID             string
	Desired            string
	DesiredGeneration  int64
	Image              string
	PreserveData       bool
	Observed           string
	ObservedGeneration int64
	EverReady          bool
	LastError          string
	LastErrorPhase     string
	Attempts           int32
	NextAttemptAt      time.Time
	LeaseOwner         string
	LeaseUntil         time.Time
	Version            int64
	UpdatedAt          time.Time
}

// Settled reports whether the observation is a stable answer to the current
// intent: no transitional state, and the generation caught up.
func (w Workspace) Settled() bool {
	if w.ObservedGeneration < w.DesiredGeneration {
		return false
	}
	switch w.Observed {
	case ObservedProvisioning, ObservedRemoving:
		return false
	}
	if w.Desired == DesiredAbsent {
		// A teardown that spent its fast retries is recorded as failed; that
		// is a definite (negative) answer, even though slow retries continue.
		return w.Observed == ObservedAbsent || w.Observed == ObservedFailed
	}
	return true
}

// RetryPending reports whether a failed observation is still inside its fast
// retry budget of maxAttempts. Attempts counts the budget consumed: a
// non-retryable failure consumes all of it at once. Beyond the budget the
// reconciler keeps retrying at a slow cadence, but that is background
// self-healing and no longer holds up callers.
func (w Workspace) RetryPending(maxAttempts int32) bool {
	return w.Observed == ObservedFailed && w.Attempts < maxAttempts
}

// Final reports whether the observation is the last word on the current
// intent: settled, and not a failure the reconciler is about to retry soon.
// Callers that relay an outcome to a user wait for Final so a transient
// failure that recovers on the next attempt never surfaces as a failure.
func (w Workspace) Final(maxAttempts int32) bool {
	return w.Settled() && !w.RetryPending(maxAttempts)
}

// ProgressEvent mirrors the workspace setup progress the SSE creation stream
// already exposes; the reconciler relays backend progress to in-process
// subscribers so the UI keeps its live pull/create feedback.
type ProgressEvent struct {
	Type             string
	Image            string
	Message          string
	Layers           []ctr.LayerStatus
	ContainerID      string
	WorkspaceBackend string
	RuntimeBackend   string
	ContainerPath    string
	CDIDevices       []string
	Snapshotter      string
	Started          bool
	DataRestored     bool
	HasPreservedData bool
	// Terminal fields, set by the reconciler on "ready" / "error".
	Phase     string
	Err       error
	Workspace *Workspace
}

// Terminal event types emitted by the reconciler in addition to backend
// progress.
const (
	EventReady = "ready"
	EventError = "error"
)

// Inspection is the backend's answer to "does this bot's workspace exist and
// is it running".
type Inspection struct {
	Exists  bool
	Running bool
	// Image is the container's image reference when it exists.
	Image string
}

// StepError attributes a backend failure to a phase and says whether the
// reconciler should retry it. Non-retryable failures (an image that does not
// exist, a template bootstrap that cannot succeed without user action) go
// straight to the failed state without consuming the retry budget.
type StepError struct {
	Phase     string
	Retryable bool
	Err       error
}

func (e *StepError) Error() string {
	if e.Err == nil {
		return e.Phase + " failed"
	}
	return e.Phase + ": " + e.Err.Error()
}

func (e *StepError) Unwrap() error { return e.Err }

// ErrVersionConflict is returned by the repository when the row changed under
// the caller. The caller drops the pass; the next one re-reads and re-decides.
var ErrVersionConflict = errors.New("bot workspace changed concurrently")

// ErrNotFound is returned when the bot has no workspace row.
var ErrNotFound = errors.New("bot workspace not found")
