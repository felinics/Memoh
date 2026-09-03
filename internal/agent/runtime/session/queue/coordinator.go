package queue

import (
	"context"
	"errors"
	"fmt"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type StepKind string

const (
	StepToolLoop         StepKind = "tool_loop"
	StepFinal            StepKind = "final"
	StepDeferredDecision StepKind = "deferred_decision"
)

type StepAction string

const (
	Continue          StepAction = "continue"
	ContinueWithSteer StepAction = "continue_with_steer"
	ParkDecision      StepAction = "park_decision"
	StartContinuation StepAction = "start_continuation"
	StopCurrent       StepAction = "stop_current"
)

type CommitStepRequest struct {
	Run        sessionruntime.RunHandle
	StepIndex  int64
	CommitHash string
	Kind       StepKind
	History    func(context.Context) error
	Persist    func(context.Context, dbstore.Queries) error
	// FinalizeHistory runs only at a true final boundary after the coordinator
	// establishes that no steer keeps the current run active.
	FinalizeHistory func(context.Context, dbstore.Queries) error
	// PersistAppliedSteer persists an existing claim after its apply CAS and
	// before the transaction records its replay result.
	PersistAppliedSteer func(context.Context, dbstore.Queries, SteerItem) error
	Steer               *SteerClaimRef
	FollowUp            *FollowUpClaimRef
}

type CommitStepResult struct {
	Action            StepAction
	Steer             *SteerItem
	SteerClaim        *SteerClaimRef
	FollowUp          *FollowUpItem
	FollowUpClaim     *FollowUpClaimRef
	ContinuationRunID string
}

// Coordinator owns the atomic boundary between persisted history, claim
// application, run terminalization, and continuation handoff.
type Coordinator interface {
	CommitStep(context.Context, CommitStepRequest) (CommitStepResult, error)
}

type LostContinuationRejector interface {
	RejectLostContinuation(context.Context, string, int64) error
}

type LostQueuedRunRejector interface {
	RejectLostQueuedRun(context.Context, string, int64) error
}

type TerminalReconciler interface {
	ReconcileTerminalRun(context.Context, sessionruntime.TerminalRun) error
}

type ContinuationFactory interface {
	CreateContinuation(context.Context, sessionruntime.RunHandle, FollowUpItem) (string, error)
}

var ErrStepConflict = errors.New("queue: step commit conflicts with durable replay")

func normalizedCommitHash(req CommitStepRequest) string {
	if req.CommitHash != "" {
		return req.CommitHash
	}
	return fmt.Sprintf("legacy:%s:%d:%s", req.Run.RunID, req.StepIndex, req.Kind)
}
