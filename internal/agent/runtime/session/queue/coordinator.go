package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"

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
	// has established that no steer keeps the current run active. PostgreSQL
	// executes it in the same transaction as R0 terminalization and follow-up
	// handoff.
	FinalizeHistory func(context.Context, dbstore.Queries) error
	// PersistAppliedSteer persists the steer whose existing claim is being
	// applied by this step. It runs after the claim CAS and before the
	// transaction records its replay result, so history cannot expose an input
	// that later becomes rejected because the run was lost.
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
// application, run terminalization, and continuation handoff. Callers cannot
// safely reproduce this boundary with separate store operations.
type Coordinator interface {
	CommitStep(context.Context, CommitStepRequest) (CommitStepResult, error)
}

// LostContinuationRejector is the recovery boundary used when a continuation
// was acquired but its execution control could not be started.
type LostContinuationRejector interface {
	RejectLostContinuation(context.Context, string, int64) error
}

// LostQueuedRunRejector closes a run that was acquired for queue recovery but
// could not be started. It rejects the claimed steer in the same transaction
// as the fenced terminal transition.
type LostQueuedRunRejector interface {
	RejectLostQueuedRun(context.Context, string, int64) error
}

// TerminalReconciler closes queue items that can no longer be consumed by a
// terminal run. It is deliberately separate from continuation rejection: an R0
// must not reject a follow-up already assigned to its ownerless R1.
type TerminalReconciler interface {
	ReconcileTerminalRun(context.Context, sessionruntime.TerminalRun) error
}

// ContinuationFactory is the session-ledger adapter used at the final
// boundary. Creation is idempotent on the source follow-up item and leaves the
// continuation ownerless while retaining the session admission slot.
type ContinuationFactory interface {
	CreateContinuation(context.Context, sessionruntime.RunHandle, FollowUpItem) (string, error)
}

// MemoryCoordinator is a deterministic unit-test model for queue ordering and
// handoff decisions. It is intentionally not a production persistence path:
// callers that need history atomicity must use PostgresCoordinator, whose
// callback runs against the same PostgreSQL transaction as queue mutations.
type MemoryCoordinator struct {
	mu       sync.Mutex
	steer    *SteerQueue
	follow   *FollowUpQueue
	terminal map[string]bool
	next     uint64
	factory  ContinuationFactory
	commits  map[string]memoryStepCommit
}

type memoryStepCommit struct {
	hash   string
	result CommitStepResult
}

var ErrStepConflict = errors.New("queue: step commit conflicts with durable replay")

func NewMemoryCoordinator(s *SteerQueue, f *FollowUpQueue) *MemoryCoordinator {
	return &MemoryCoordinator{steer: s, follow: f, terminal: map[string]bool{}, commits: map[string]memoryStepCommit{}}
}

func (c *MemoryCoordinator) WithContinuationFactory(factory ContinuationFactory) *MemoryCoordinator {
	c.factory = factory
	return c
}

func (c *MemoryCoordinator) CommitStep(ctx context.Context, req CommitStepRequest) (CommitStepResult, error) {
	if err := validateRun(req.Run); err != nil {
		return CommitStepResult{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := fmt.Sprintf("%s/%d", req.Run.RunID, req.StepIndex)
	hash := normalizedCommitHash(req)
	if err := c.validateClaims(req); err != nil {
		return CommitStepResult{}, err
	}
	if previous, ok := c.commits[key]; ok {
		if previous.hash != hash {
			return CommitStepResult{}, ErrStepConflict
		}
		return previous.result, nil
	}
	commit := func(result CommitStepResult) (CommitStepResult, error) {
		c.commits[key] = memoryStepCommit{hash: hash, result: result}
		return result, nil
	}
	if c.terminal[req.Run.RunID] {
		return CommitStepResult{}, sessionruntime.ErrRunOwnershipLost
	}
	if req.History != nil {
		if err := req.History(ctx); err != nil {
			return CommitStepResult{}, err
		}
	}
	if req.Steer != nil {
		item, ok := c.steer.Get(req.Steer.ItemID)
		if !ok {
			return CommitStepResult{}, ErrInvalidReference
		}
		if err := c.steer.Apply(*req.Steer, req.Run); err != nil {
			return CommitStepResult{}, err
		}
		if req.PersistAppliedSteer != nil {
			if err := req.PersistAppliedSteer(ctx, nil, item); err != nil {
				return CommitStepResult{}, err
			}
		}
	}
	if req.FollowUp != nil {
		if err := c.follow.Apply(*req.FollowUp, req.Run); err != nil {
			return CommitStepResult{}, err
		}
	}
	if req.Persist != nil {
		if err := req.Persist(ctx, nil); err != nil {
			return CommitStepResult{}, err
		}
	}
	if req.Kind == StepDeferredDecision {
		return commit(CommitStepResult{Action: ParkDecision})
	}
	if item, ref, err := c.steer.Claim(req.Run); err == nil {
		return commit(CommitStepResult{Action: ContinueWithSteer, Steer: &item, SteerClaim: &ref})
	}
	if req.Kind == StepToolLoop {
		return commit(CommitStepResult{Action: Continue})
	}
	if req.FinalizeHistory != nil {
		if err := req.FinalizeHistory(ctx, nil); err != nil {
			return CommitStepResult{}, err
		}
	}
	item, err := c.follow.NextPending()
	if err == nil {
		c.next++
		continuation := req.Run.RunID + "/continuation/" + string(item.ID)
		assigned, assignErr := c.follow.Assign(item.ID, continuation)
		if assignErr != nil {
			return CommitStepResult{}, assignErr
		}
		item = assigned
		if c.factory != nil {
			created, createErr := c.factory.CreateContinuation(ctx, req.Run, item)
			if createErr != nil {
				_ = c.follow.Unassign(item.ID, continuation)
				return CommitStepResult{}, createErr
			}
			if created != "" {
				continuation = created
			}
		}
		return commit(CommitStepResult{Action: StartContinuation, FollowUp: &item, ContinuationRunID: continuation})
	}
	c.terminal[req.Run.RunID] = true
	return commit(CommitStepResult{Action: StopCurrent})
}

func (c *MemoryCoordinator) validateClaims(req CommitStepRequest) error {
	if req.Steer != nil {
		item, ok := c.steer.items[req.Steer.ItemID]
		if !ok || item.Status != Claimed || item.Claim == nil || *item.Claim != *req.Steer || item.TargetRunID != req.Run.RunID {
			return ErrInvalidReference
		}
	}
	if req.FollowUp != nil {
		item, ok := c.follow.items[req.FollowUp.ItemID]
		if !ok || item.Status != Claimed || item.Claim == nil || *item.Claim != *req.FollowUp || item.AssignedRunID != req.Run.RunID {
			return ErrInvalidReference
		}
	}
	return nil
}

func normalizedCommitHash(req CommitStepRequest) string {
	if req.CommitHash != "" {
		return req.CommitHash
	}
	// Claim references authorize queue state transitions; they are not part of
	// the step's durable content. Excluding them keeps a replay request that
	// omits already-consumed refs idempotent while validation remains explicit
	// in each coordinator before replay.
	return fmt.Sprintf("legacy:%s:%d:%s", req.Run.RunID, req.StepIndex, req.Kind)
}
