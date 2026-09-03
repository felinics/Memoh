package queue

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	db "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// TxRunner is supplied by the PostgreSQL store so the coordinator never owns
// a second transaction implementation.
type TxRunner func(context.Context, func(dbstore.Queries) error) error

type PostgresCoordinator struct {
	q  dbstore.Queries
	tx TxRunner
}

func NewPostgresCoordinator(q dbstore.Queries, tx TxRunner) *PostgresCoordinator {
	return &PostgresCoordinator{q: q, tx: tx}
}

// RejectLostContinuation atomically marks the ownerless or fenced
// continuation lost and rejects its source follow-up.
func (c *PostgresCoordinator) RejectLostContinuation(ctx context.Context, runID string, fencingToken int64) error {
	if c == nil || c.q == nil || c.tx == nil {
		return errors.New("queue: postgres coordinator is not configured")
	}
	return c.tx(ctx, func(txq dbstore.Queries) error {
		return NewPostgresStore(txq).RejectContinuation(ctx, runID, fencingToken)
	})
}

func (c *PostgresCoordinator) RejectLostQueuedRun(ctx context.Context, runID string, fencingToken int64) error {
	if c == nil || c.q == nil || c.tx == nil {
		return errors.New("queue: postgres coordinator is not configured")
	}
	return c.tx(ctx, func(txq dbstore.Queries) error {
		rid, err := db.ParseUUID(runID)
		if err != nil {
			return err
		}
		if err := txq.RejectSteerItemsForRun(ctx, rid); err != nil {
			return err
		}
		_, err = txq.FinalizeSessionRun(ctx, dbsqlc.FinalizeSessionRunParams{
			State: "lost", RunID: rid, FencingToken: fencingToken,
			ErrorCode:    pgtype.Text{String: "queue_steer_recovery_failed", Valid: true},
			ErrorMessage: pgtype.Text{String: "steer queue recovery failed", Valid: true},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
}

// ReconcileTerminalRun rejects queue work that can no longer be consumed.
// Successful final commits deliberately retain unassigned session follow-ups:
// each continuation hands the next FIFO item to another continuation. Failed
// or lost chains reject their remaining pending work instead of transferring
// it to an unrelated later admission.
func (c *PostgresCoordinator) ReconcileTerminalRun(ctx context.Context, terminal sessionruntime.TerminalRun) error {
	if c == nil || c.q == nil || c.tx == nil || terminal.RunID == "" {
		return errors.New("queue: postgres coordinator is not configured")
	}
	return c.tx(ctx, func(txq dbstore.Queries) error {
		runID, err := db.ParseUUID(terminal.RunID)
		if err != nil {
			return err
		}
		if err := txq.RejectSteerItemsForRun(ctx, runID); err != nil {
			return err
		}
		row, err := txq.GetSessionRun(ctx, runID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.SourceFollowUpItemID.Valid {
			if err := txq.RejectFollowUpForContinuation(ctx, runID); err != nil {
				return err
			}
			if terminal.State != "completed" {
				return txq.RejectUnassignedFollowUpsForSession(ctx, row.SessionID)
			}
			return nil
		}
		if terminal.State != "completed" {
			return txq.RejectUnassignedFollowUpsForRun(ctx, runID)
		}
		return nil
	})
}

func (c *PostgresCoordinator) CommitStep(ctx context.Context, req CommitStepRequest) (result CommitStepResult, err error) {
	if c == nil || c.q == nil || c.tx == nil {
		return result, errors.New("queue: postgres coordinator is not configured")
	}
	if err := validateRunHandle(req.Run); err != nil {
		return result, err
	}
	err = c.tx(ctx, func(txq dbstore.Queries) error {
		botID, e := db.ParseUUID(req.Run.BotID)
		if e != nil {
			return e
		}
		sessionID, e := db.ParseUUID(req.Run.SessionID)
		if e != nil {
			return e
		}
		runID, e := db.ParseUUID(req.Run.RunID)
		if e != nil {
			return e
		}
		if _, e = txq.LockSessionForCommitReconciliation(ctx, dbsqlc.LockSessionForCommitReconciliationParams{BotID: botID, SessionID: sessionID}); e != nil {
			return e
		}
		store := NewPostgresStore(txq)
		commitHash := normalizedCommitHash(req)
		existingCommit, e := txq.GetSessionQueueStepCommit(ctx, dbsqlc.GetSessionQueueStepCommitParams{
			RunID: runID, StepIndex: req.StepIndex,
		})
		if e == nil {
			if existingCommit.CommitHash != commitHash {
				return ErrStepConflict
			}
			result, e = replayCommitStepResult(ctx, NewPostgresStore(txq), existingCommit)
			return e
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if _, e = txq.LockSessionRunForAgentStepCommit(ctx, dbsqlc.LockSessionRunForAgentStepCommitParams{
			RunID: runID, BotID: botID, SessionID: sessionID, FencingToken: req.Run.FencingToken,
		}); errors.Is(e, pgx.ErrNoRows) {
			return sessionruntime.ErrRunOwnershipLost
		} else if e != nil {
			return e
		}
		if req.Steer != nil {
			appliedSteer, applyErr := store.ApplySteerItem(ctx, *req.Steer)
			if applyErr != nil {
				return applyErr
			}
			if req.PersistAppliedSteer != nil {
				if e = req.PersistAppliedSteer(ctx, txq, appliedSteer); e != nil {
					return e
				}
			}
		}
		if req.FollowUp != nil {
			if e = store.ApplyFollowUp(ctx, *req.FollowUp); e != nil {
				return e
			}
		}
		if req.Persist != nil {
			if e = req.Persist(ctx, txq); e != nil {
				return e
			}
		}
		if req.Kind == StepDeferredDecision {
			result = CommitStepResult{Action: ParkDecision}
			return persistCommitStepResult(ctx, txq, runID, req.StepIndex, commitHash, result)
		}
		claimed, claimErr := store.ClaimNextSteer(ctx, req.Run.SessionID, req.Run)
		if claimErr == nil {
			ref := SteerClaimRef{ItemID: claimed.ID, RunID: req.Run.RunID, OwnerID: req.Run.OwnerID, FencingToken: req.Run.FencingToken}
			result = CommitStepResult{Action: ContinueWithSteer, Steer: &claimed, SteerClaim: &ref}
			return persistCommitStepResult(ctx, txq, runID, req.StepIndex, commitHash, result)
		}
		if !errors.Is(claimErr, ErrNotPending) {
			return claimErr
		}
		if req.Kind == StepToolLoop {
			result = CommitStepResult{Action: Continue}
			return persistCommitStepResult(ctx, txq, runID, req.StepIndex, commitHash, result)
		}
		if req.FinalizeHistory != nil {
			if e = req.FinalizeHistory(ctx, txq); e != nil {
				return e
			}
		}
		_, e = txq.FinalizeSessionRun(ctx, dbsqlc.FinalizeSessionRunParams{State: "completed", RunID: runID, FencingToken: req.Run.FencingToken})
		if errors.Is(e, pgx.ErrNoRows) {
			return sessionruntime.ErrRunOwnershipLost
		}
		if e != nil {
			return e
		}
		item, followErr := store.NextPendingFollowUp(ctx, req.Run.SessionID)
		if followErr == nil {
			runIDString, e := store.CreateContinuation(ctx, req.Run, item)
			if e != nil {
				return e
			}
			result = CommitStepResult{Action: StartContinuation, FollowUp: &item, ContinuationRunID: runIDString}
			return persistCommitStepResult(ctx, txq, runID, req.StepIndex, commitHash, result)
		}
		if !errors.Is(followErr, ErrNotPending) {
			return followErr
		}
		result = CommitStepResult{Action: StopCurrent}
		return persistCommitStepResult(ctx, txq, runID, req.StepIndex, commitHash, result)
	})
	return result, err
}

func persistCommitStepResult(
	ctx context.Context,
	q dbstore.Queries,
	runID pgtype.UUID,
	stepIndex int64,
	commitHash string,
	result CommitStepResult,
) error {
	params := dbsqlc.CreateSessionQueueStepCommitParams{
		RunID: runID, StepIndex: stepIndex, CommitHash: commitHash, Action: string(result.Action),
	}
	var err error
	if result.Steer != nil {
		params.SteerItemID, err = db.ParseUUID(string(result.Steer.ID))
		if err != nil {
			return err
		}
	}
	if result.FollowUp != nil {
		params.FollowUpItemID, err = db.ParseUUID(string(result.FollowUp.ID))
		if err != nil {
			return err
		}
	}
	if result.ContinuationRunID != "" {
		params.ContinuationRunID, err = db.ParseUUID(result.ContinuationRunID)
		if err != nil {
			return err
		}
	}
	_, err = q.CreateSessionQueueStepCommit(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, lookupErr := q.GetSessionQueueStepCommit(ctx, dbsqlc.GetSessionQueueStepCommitParams{
			RunID: runID, StepIndex: stepIndex,
		})
		if lookupErr != nil {
			return lookupErr
		}
		if existing.CommitHash != commitHash {
			return ErrStepConflict
		}
		return nil
	}
	return err
}

func replayCommitStepResult(ctx context.Context, store *PostgresStore, commit dbsqlc.SessionQueueStepCommit) (CommitStepResult, error) {
	result := CommitStepResult{Action: StepAction(commit.Action)}
	if commit.SteerItemID.Valid {
		item, err := store.SteerByID(ctx, SteerItemID(commit.SteerItemID.String()))
		if err != nil {
			return CommitStepResult{}, err
		}
		result.Steer = &item
		result.SteerClaim = item.Claim
	}
	if commit.FollowUpItemID.Valid {
		item, err := store.FollowUpByID(ctx, FollowUpItemID(commit.FollowUpItemID.String()))
		if err != nil {
			return CommitStepResult{}, err
		}
		result.FollowUp = &item
		result.FollowUpClaim = item.Claim
	}
	if commit.ContinuationRunID.Valid {
		result.ContinuationRunID = commit.ContinuationRunID.String()
	}
	return result, nil
}

var (
	_ Coordinator           = (*PostgresCoordinator)(nil)
	_ TerminalReconciler    = (*PostgresCoordinator)(nil)
	_ LostQueuedRunRejector = (*PostgresCoordinator)(nil)
	_ ContinuationFactory   = (*PostgresStore)(nil)
)
