package queue

// This file is the PostgreSQL primitive adapter. Transaction ownership remains
// with the caller (normally runtimefence.InTransaction or the session ledger
// coordinator); every method below is a single fenced SQL statement and never
// keeps process-local queue state.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	db "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type PostgresStore struct{ q dbstore.Queries }

type ContinuationRun struct {
	RunID, BotID, SessionID, TurnID string
	TurnPosition                    int64
}

type OwnerlessContinuation struct {
	RunID                string
	SourceFollowUpItemID FollowUpItemID
}

// RecoverableRun is the durable input needed to restart an execution-owned
// run after its live owner lease expired. Queue recovery only returns runs with
// a claimed steer item; ordinary runs remain under the runtime reaper's lost
// semantics.
type RecoverableRun struct {
	RunID, BotID, SessionID, TurnID string
	TurnPosition                    int64
	InvocationID                    string
	Input                           []byte
	FencingToken                    int64
}

func NewPostgresStore(q dbstore.Queries) *PostgresStore { return &PostgresStore{q: q} }

func validateRunHandle(run sessionruntime.RunHandle) error {
	if strings.TrimSpace(run.RunID) == "" || strings.TrimSpace(run.OwnerID) == "" || run.FencingToken <= 0 {
		return errors.New("queue: owned run handle is required")
	}
	return nil
}

func (s *PostgresStore) EnqueueSteer(ctx context.Context, botID, sessionID, itemID, invocationID string, payload []byte) (SteerItem, error) {
	if s == nil || s.q == nil {
		return SteerItem{}, errors.New("queue: postgres store is not configured")
	}
	b, err := db.ParseUUID(botID)
	if err != nil {
		return SteerItem{}, err
	}
	sid, err := db.ParseUUID(sessionID)
	if err != nil {
		return SteerItem{}, err
	}
	id, err := db.ParseUUID(itemID)
	if err != nil {
		return SteerItem{}, err
	}
	existing, err := s.q.GetSteerQueueItemByInvocation(ctx, dbsqlc.GetSteerQueueItemByInvocationParams{SessionID: sid, InvocationID: invocationID})
	if err == nil {
		if !jsonPayloadEqual(existing.Payload, payload) {
			return SteerItem{}, ErrInvocationConflict
		}
		return steerFromRow(existing), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return SteerItem{}, err
	}
	row, err := s.q.EnqueueSteerQueueItem(ctx, dbsqlc.EnqueueSteerQueueItemParams{ItemID: id, BotID: b, SessionID: sid, InvocationID: invocationID, Payload: payload})
	if errors.Is(err, pgx.ErrNoRows) {
		// DO NOTHING is intentional: a concurrent retry with the same
		// invocation must be compared against the durable payload instead of
		// being silently accepted as a different request.
		existing, lookupErr := s.q.GetSteerQueueItemByInvocation(ctx, dbsqlc.GetSteerQueueItemByInvocationParams{SessionID: sid, InvocationID: invocationID})
		if lookupErr == nil {
			if !jsonPayloadEqual(existing.Payload, payload) {
				return SteerItem{}, ErrInvocationConflict
			}
			return steerFromRow(existing), nil
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return SteerItem{}, lookupErr
		}
		return SteerItem{}, ErrNoActiveRun
	}
	if err != nil {
		return SteerItem{}, err
	}
	return steerFromRow(row), nil
}

func (s *PostgresStore) EnqueueFollowUp(ctx context.Context, botID, sessionID, itemID, invocationID string, payload []byte) (FollowUpItem, error) {
	if s == nil || s.q == nil {
		return FollowUpItem{}, errors.New("queue: postgres store is not configured")
	}
	b, err := db.ParseUUID(botID)
	if err != nil {
		return FollowUpItem{}, err
	}
	sid, err := db.ParseUUID(sessionID)
	if err != nil {
		return FollowUpItem{}, err
	}
	id, err := db.ParseUUID(itemID)
	if err != nil {
		return FollowUpItem{}, err
	}
	existing, err := s.q.GetFollowUpQueueItemByInvocation(ctx, dbsqlc.GetFollowUpQueueItemByInvocationParams{SessionID: sid, InvocationID: invocationID})
	if err == nil {
		if !jsonPayloadEqual(existing.Payload, payload) {
			return FollowUpItem{}, ErrInvocationConflict
		}
		return followFromRow(existing), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return FollowUpItem{}, err
	}
	row, err := s.q.EnqueueFollowUpQueueItem(ctx, dbsqlc.EnqueueFollowUpQueueItemParams{ItemID: id, BotID: b, SessionID: sid, InvocationID: invocationID, Payload: payload})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, lookupErr := s.q.GetFollowUpQueueItemByInvocation(ctx, dbsqlc.GetFollowUpQueueItemByInvocationParams{SessionID: sid, InvocationID: invocationID})
		if lookupErr == nil {
			if !jsonPayloadEqual(existing.Payload, payload) {
				return FollowUpItem{}, ErrInvocationConflict
			}
			return followFromRow(existing), nil
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return FollowUpItem{}, lookupErr
		}
		return FollowUpItem{}, ErrNoActiveRun
	}
	if err != nil {
		return FollowUpItem{}, err
	}
	return followFromRow(row), nil
}

func (s *PostgresStore) PendingSteer(ctx context.Context, sessionID string) ([]SteerItem, error) {
	sid, err := db.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListPendingSteerQueue(ctx, sid)
	if err != nil {
		return nil, err
	}
	out := make([]SteerItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, steerFromRow(row))
	}
	return out, nil
}

func (s *PostgresStore) PendingFollowUp(ctx context.Context, sessionID string) ([]FollowUpItem, error) {
	sid, err := db.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListPendingFollowUpQueue(ctx, sid)
	if err != nil {
		return nil, err
	}
	out := make([]FollowUpItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, followFromRow(row))
	}
	return out, nil
}

func (s *PostgresStore) FollowUpByID(ctx context.Context, itemID FollowUpItemID) (FollowUpItem, error) {
	id, err := db.ParseUUID(string(itemID))
	if err != nil {
		return FollowUpItem{}, err
	}
	row, err := s.q.GetFollowUpQueueItem(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return FollowUpItem{}, ErrInvalidReference
	}
	if err != nil {
		return FollowUpItem{}, err
	}
	return followFromRow(row), nil
}

func (s *PostgresStore) SteerByID(ctx context.Context, itemID SteerItemID) (SteerItem, error) {
	id, err := db.ParseUUID(string(itemID))
	if err != nil {
		return SteerItem{}, err
	}
	row, err := s.q.GetSteerQueueItem(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return SteerItem{}, ErrInvalidReference
	}
	if err != nil {
		return SteerItem{}, err
	}
	return steerFromRow(row), nil
}

// UpdateSteer edits only an accepted, still-pending steer item. The caller
// owns the session admission transaction and must hold its lock.
func (s *PostgresStore) UpdateSteer(ctx context.Context, sessionID, itemID string, payload []byte) (SteerItem, error) {
	sid, err := db.ParseUUID(sessionID)
	if err != nil {
		return SteerItem{}, err
	}
	id, err := db.ParseUUID(itemID)
	if err != nil {
		return SteerItem{}, err
	}
	row, err := s.q.UpdateAcceptedSteerQueueItemPayload(ctx, dbsqlc.UpdateAcceptedSteerQueueItemPayloadParams{SessionID: sid, ItemID: id, Payload: payload})
	if errors.Is(err, pgx.ErrNoRows) {
		return SteerItem{}, ErrNotPending
	}
	if err != nil {
		return SteerItem{}, err
	}
	return steerFromRow(row), nil
}

func (s *PostgresStore) CancelSteer(ctx context.Context, sessionID, itemID string) error {
	sid, err := db.ParseUUID(sessionID)
	if err != nil {
		return err
	}
	id, err := db.ParseUUID(itemID)
	if err != nil {
		return err
	}
	_, err = s.q.CancelAcceptedSteerQueueItem(ctx, dbsqlc.CancelAcceptedSteerQueueItemParams{SessionID: sid, ItemID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotPending
	}
	return err
}

func (s *PostgresStore) UpdateFollowUp(ctx context.Context, sessionID, itemID string, payload []byte) (FollowUpItem, error) {
	sid, err := db.ParseUUID(sessionID)
	if err != nil {
		return FollowUpItem{}, err
	}
	id, err := db.ParseUUID(itemID)
	if err != nil {
		return FollowUpItem{}, err
	}
	row, err := s.q.UpdateAcceptedFollowUpQueueItemPayload(ctx, dbsqlc.UpdateAcceptedFollowUpQueueItemPayloadParams{SessionID: sid, ItemID: id, Payload: payload})
	if errors.Is(err, pgx.ErrNoRows) {
		return FollowUpItem{}, ErrNotPending
	}
	if err != nil {
		return FollowUpItem{}, err
	}
	return followFromRow(row), nil
}

func (s *PostgresStore) CancelFollowUp(ctx context.Context, sessionID, itemID string) error {
	sid, err := db.ParseUUID(sessionID)
	if err != nil {
		return err
	}
	id, err := db.ParseUUID(itemID)
	if err != nil {
		return err
	}
	_, err = s.q.CancelAcceptedFollowUpQueueItem(ctx, dbsqlc.CancelAcceptedFollowUpQueueItemParams{SessionID: sid, ItemID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotPending
	}
	return err
}

// ReorderSteer moves one accepted steer before another. The caller must hold
// the session admission lock and keep all updates in the same transaction.
func (s *PostgresStore) ReorderSteer(ctx context.Context, sessionID string, item, before SteerPendingRef) ([]SteerItem, error) {
	sid, err := db.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	if _, err = s.q.LockSessionForQueueMutation(ctx, sid); err != nil {
		return nil, err
	}
	items, err := s.PendingSteer(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	pending := make(map[SteerItemID]*SteerItem, len(items))
	for i := range items {
		copyItem := items[i]
		pending[copyItem.ID] = &copyItem
	}
	if err = reorderSteer(pending, item.ItemID, before.ItemID); err != nil {
		return nil, err
	}
	ordered := sortedSteer(pending)
	for _, queued := range ordered {
		id, parseErr := db.ParseUUID(string(queued.ID))
		if parseErr != nil {
			return nil, parseErr
		}
		updated, updateErr := s.q.UpdateAcceptedSteerQueuePosition(ctx, dbsqlc.UpdateAcceptedSteerQueuePositionParams{Position: queued.Position, SessionID: sid, ItemID: id})
		if updateErr != nil {
			return nil, updateErr
		}
		if updated != 1 {
			return nil, ErrNotPending
		}
	}
	result := make([]SteerItem, 0, len(ordered))
	for _, queued := range ordered {
		result = append(result, cloneSteer(*queued))
	}
	return result, nil
}

// ReorderFollowUp moves one unassigned accepted follow-up before another.
// Assignment and claiming therefore close the reorder window atomically.
func (s *PostgresStore) ReorderFollowUp(ctx context.Context, sessionID string, item, before FollowUpPendingRef) ([]FollowUpItem, error) {
	sid, err := db.ParseUUID(sessionID)
	if err != nil {
		return nil, err
	}
	if _, err = s.q.LockSessionForQueueMutation(ctx, sid); err != nil {
		return nil, err
	}
	items, err := s.PendingFollowUp(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	pending := make(map[FollowUpItemID]*FollowUpItem, len(items))
	for i := range items {
		copyItem := items[i]
		pending[copyItem.ID] = &copyItem
	}
	if err = reorderFollowUp(pending, item.ItemID, before.ItemID); err != nil {
		return nil, err
	}
	ordered := sortedFollowUp(pending)
	for _, queued := range ordered {
		id, parseErr := db.ParseUUID(string(queued.ID))
		if parseErr != nil {
			return nil, parseErr
		}
		updated, updateErr := s.q.UpdateAcceptedFollowUpQueuePosition(ctx, dbsqlc.UpdateAcceptedFollowUpQueuePositionParams{Position: queued.Position, SessionID: sid, ItemID: id})
		if updateErr != nil {
			return nil, updateErr
		}
		if updated != 1 {
			return nil, ErrNotPending
		}
	}
	result := make([]FollowUpItem, 0, len(ordered))
	for _, queued := range ordered {
		result = append(result, cloneFollowUp(*queued))
	}
	return result, nil
}

func (s *PostgresStore) ListOwnerlessContinuations(ctx context.Context, batchSize int32) ([]OwnerlessContinuation, error) {
	if batchSize <= 0 {
		batchSize = 100
	}
	rows, err := s.q.ListOwnerlessContinuationRuns(ctx, batchSize)
	if err != nil {
		return nil, err
	}
	result := make([]OwnerlessContinuation, 0, len(rows))
	for _, row := range rows {
		if !row.SourceFollowUpItemID.Valid {
			continue
		}
		result = append(result, OwnerlessContinuation{
			RunID:                row.RunID.String(),
			SourceFollowUpItemID: FollowUpItemID(row.SourceFollowUpItemID.String()),
		})
	}
	return result, nil
}

func (s *PostgresStore) GetContinuationRun(ctx context.Context, runID string) (ContinuationRun, error) {
	rid, err := db.ParseUUID(runID)
	if err != nil {
		return ContinuationRun{}, err
	}
	row, err := s.q.GetSessionRun(ctx, rid)
	if err != nil {
		return ContinuationRun{}, err
	}
	if !row.SourceFollowUpItemID.Valid {
		return ContinuationRun{}, ErrInvalidReference
	}
	return ContinuationRun{RunID: row.RunID.String(), BotID: row.BotID.String(), SessionID: row.SessionID.String(), TurnID: row.TurnID.String(), TurnPosition: row.TurnPosition}, nil
}

func (s *PostgresStore) GetClaimedSteerForRun(ctx context.Context, runID string) (SteerItem, error) {
	rid, err := db.ParseUUID(runID)
	if err != nil {
		return SteerItem{}, err
	}
	row, err := s.q.GetClaimedSteerQueueItemForRun(ctx, rid)
	if errors.Is(err, pgx.ErrNoRows) {
		return SteerItem{}, ErrNotPending
	}
	if err != nil {
		return SteerItem{}, err
	}
	return steerFromRow(row), nil
}

func (s *PostgresStore) GetRecoverableRun(ctx context.Context, runID string) (RecoverableRun, error) {
	rid, err := db.ParseUUID(runID)
	if err != nil {
		return RecoverableRun{}, err
	}
	row, err := s.q.GetSessionRun(ctx, rid)
	if err != nil {
		return RecoverableRun{}, err
	}
	return RecoverableRun{
		RunID: row.RunID.String(), BotID: row.BotID.String(), SessionID: row.SessionID.String(),
		TurnID: row.TurnID.String(), TurnPosition: row.TurnPosition, InvocationID: row.InvocationID,
		Input: append([]byte(nil), row.InputJson...), FencingToken: row.FencingToken,
	}, nil
}

func (s *PostgresStore) NextQueueStepIndex(ctx context.Context, runID string) (int, error) {
	rid, err := db.ParseUUID(runID)
	if err != nil {
		return 0, err
	}
	next, err := s.q.NextSessionQueueStepIndex(ctx, rid)
	if err != nil {
		return 0, err
	}
	return int(next), nil
}

// AcquireQueuedRun conditionally moves ownership to this process and advances
// the canonical fencing token. The previous token comes from the expired live
// lease candidate, so a concurrent successor cannot be displaced.
func (s *PostgresStore) AcquireQueuedRun(ctx context.Context, runID string, previousToken int64, ownerID, generation string) (sessionruntime.RunHandle, bool, error) {
	ownerID = strings.TrimSpace(ownerID)
	generation = strings.TrimSpace(generation)
	if ownerID == "" || generation == "" || previousToken <= 0 {
		return sessionruntime.RunHandle{}, false, errors.New("queue: queued run recovery owner and token are required")
	}
	rid, err := db.ParseUUID(runID)
	if err != nil {
		return sessionruntime.RunHandle{}, false, err
	}
	row, err := s.q.AcquireQueuedRun(ctx, dbsqlc.AcquireQueuedRunParams{
		RunID: rid, PreviousFencingToken: previousToken,
		ExecutionOwnerID: pgtype.Text{String: ownerID, Valid: true},
		LiveGeneration:   pgtype.Text{String: generation, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return sessionruntime.RunHandle{}, false, nil
	}
	if err != nil {
		return sessionruntime.RunHandle{}, false, err
	}
	return sessionruntime.RunHandle{
		BotID:        row.BotID.String(),
		SessionID:    row.SessionID.String(),
		RunID:        row.RunID.String(),
		OwnerID:      ownerID,
		TurnID:       row.TurnID.String(),
		FencingToken: row.FencingToken,
	}, true, nil
}

// AcquireContinuationRun conditionally assigns the existing session run owner
// and advances its canonical fencing token. It does not create a queue lease.
func (s *PostgresStore) AcquireContinuationRun(ctx context.Context, runID, ownerID, liveGeneration string) (sessionruntime.RunHandle, bool, error) {
	ownerID = strings.TrimSpace(ownerID)
	liveGeneration = strings.TrimSpace(liveGeneration)
	if ownerID == "" || liveGeneration == "" {
		return sessionruntime.RunHandle{}, false, errors.New("queue: continuation owner and generation are required")
	}
	rid, err := db.ParseUUID(runID)
	if err != nil {
		return sessionruntime.RunHandle{}, false, err
	}
	row, err := s.q.AcquireContinuationRun(ctx, dbsqlc.AcquireContinuationRunParams{
		ContinuationRunID: rid,
		ExecutionOwnerID:  pgtype.Text{String: ownerID, Valid: true},
		LiveGeneration:    pgtype.Text{String: liveGeneration, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return sessionruntime.RunHandle{}, false, nil
	}
	if err != nil {
		return sessionruntime.RunHandle{}, false, err
	}
	return sessionruntime.RunHandle{
		BotID: row.BotID.String(), SessionID: row.SessionID.String(), RunID: row.RunID.String(),
		OwnerID: ownerID, TurnID: row.TurnID.String(), FencingToken: row.FencingToken,
	}, true, nil
}

// RejectContinuation must run inside the coordinator transaction so rejecting
// the source item and terminalizing its continuation cannot split.
func (s *PostgresStore) RejectContinuation(ctx context.Context, runID string, fencingToken int64) error {
	rid, err := db.ParseUUID(runID)
	if err != nil {
		return err
	}
	if err = s.q.RejectFollowUpForContinuation(ctx, rid); err != nil {
		return err
	}
	_, err = s.q.FinalizeSessionRun(ctx, dbsqlc.FinalizeSessionRunParams{State: "lost", RunID: rid, FencingToken: fencingToken, ErrorCode: pgtype.Text{String: "queue_continuation_lost", Valid: true}, ErrorMessage: pgtype.Text{String: ErrContinuationLost.Error(), Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		// A generic runtime reaper may have already terminalized the
		// continuation. Source rejection still belongs to this transaction and
		// is safe to commit when the durable row is already lost.
		row, lookupErr := s.q.GetSessionRun(ctx, rid)
		if lookupErr != nil || row.State != "lost" {
			return ErrInvalidReference
		}
		return nil
	}
	return err
}

func (s *PostgresStore) ClaimSteer(ctx context.Context, itemID string, run sessionruntime.RunHandle) (SteerItem, error) {
	if err := validateRunHandle(run); err != nil {
		return SteerItem{}, err
	}
	id, err := db.ParseUUID(itemID)
	if err != nil {
		return SteerItem{}, err
	}
	rid, err := db.ParseUUID(run.RunID)
	if err != nil {
		return SteerItem{}, err
	}
	row, err := s.q.ClaimSteerQueueItem(ctx, dbsqlc.ClaimSteerQueueItemParams{QueueItemID: id, ExecutionRunID: rid, ExecutionOwnerID: pgtype.Text{String: run.OwnerID, Valid: true}, ExecutionFencingToken: pgtype.Int8{Int64: run.FencingToken, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return SteerItem{}, ErrNotPending
	}
	if err != nil {
		return SteerItem{}, err
	}
	return steerFromRow(row), nil
}

func (s *PostgresStore) ReclaimSteer(ctx context.Context, itemID string, run sessionruntime.RunHandle) (SteerItem, SteerClaimRef, error) {
	if err := validateRunHandle(run); err != nil {
		return SteerItem{}, SteerClaimRef{}, err
	}
	id, err := db.ParseUUID(itemID)
	if err != nil {
		return SteerItem{}, SteerClaimRef{}, err
	}
	rid, err := db.ParseUUID(run.RunID)
	if err != nil {
		return SteerItem{}, SteerClaimRef{}, err
	}
	row, err := s.q.ReclaimSteerQueueItem(ctx, dbsqlc.ReclaimSteerQueueItemParams{QueueItemID: id, ExecutionRunID: rid, ExecutionOwnerID: pgtype.Text{String: run.OwnerID, Valid: true}, ExecutionFencingToken: pgtype.Int8{Int64: run.FencingToken, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return SteerItem{}, SteerClaimRef{}, ErrInvalidReference
	}
	if err != nil {
		return SteerItem{}, SteerClaimRef{}, err
	}
	item := steerFromRow(row)
	ref := SteerClaimRef{ItemID: item.ID, RunID: run.RunID, OwnerID: run.OwnerID, FencingToken: run.FencingToken}
	return item, ref, nil
}

func (s *PostgresStore) ApplySteer(ctx context.Context, ref SteerClaimRef) error {
	if strings.TrimSpace(ref.RunID) == "" || strings.TrimSpace(ref.OwnerID) == "" || ref.FencingToken <= 0 {
		return errors.New("queue: owned claim reference is required")
	}
	id, err := db.ParseUUID(string(ref.ItemID))
	if err != nil {
		return err
	}
	rid, err := db.ParseUUID(ref.RunID)
	if err != nil {
		return err
	}
	_, err = s.q.ApplySteerQueueItem(ctx, dbsqlc.ApplySteerQueueItemParams{QueueItemID: id, ExecutionRunID: rid, ExecutionOwnerID: pgtype.Text{String: ref.OwnerID, Valid: true}, ExecutionFencingToken: pgtype.Int8{Int64: ref.FencingToken, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidReference
	}
	return err
}

func (s *PostgresStore) AssignFollowUp(ctx context.Context, sessionID, runID string) (FollowUpItem, error) {
	sid, err := db.ParseUUID(sessionID)
	if err != nil {
		return FollowUpItem{}, err
	}
	rid, err := db.ParseUUID(runID)
	if err != nil {
		return FollowUpItem{}, err
	}
	row, err := s.q.AssignFollowUpQueueItem(ctx, dbsqlc.AssignFollowUpQueueItemParams{SessionID: sid, RunID: rid})
	if errors.Is(err, pgx.ErrNoRows) {
		return FollowUpItem{}, ErrNotPending
	}
	if err != nil {
		return FollowUpItem{}, err
	}
	return followFromRow(row), nil
}

func (s *PostgresStore) ClaimAssignedFollowUp(ctx context.Context, itemID string, run sessionruntime.RunHandle) (FollowUpItem, error) {
	if err := validateRunHandle(run); err != nil {
		return FollowUpItem{}, err
	}
	id, err := db.ParseUUID(itemID)
	if err != nil {
		return FollowUpItem{}, err
	}
	rid, err := db.ParseUUID(run.RunID)
	if err != nil {
		return FollowUpItem{}, err
	}
	row, err := s.q.ClaimAssignedFollowUpQueueItem(ctx, dbsqlc.ClaimAssignedFollowUpQueueItemParams{QueueItemID: id, ExecutionRunID: rid, ExecutionOwnerID: pgtype.Text{String: run.OwnerID, Valid: true}, ExecutionFencingToken: pgtype.Int8{Int64: run.FencingToken, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return FollowUpItem{}, ErrInvalidReference
	}
	if err != nil {
		return FollowUpItem{}, err
	}
	item := followFromRow(row)
	ref := FollowUpClaimRef{ItemID: item.ID, RunID: run.RunID, OwnerID: run.OwnerID, FencingToken: run.FencingToken}
	item.Claim = &ref
	return item, nil
}

func (s *PostgresStore) ReclaimAssignedFollowUp(ctx context.Context, itemID string, run sessionruntime.RunHandle) (FollowUpItem, FollowUpClaimRef, error) {
	if err := validateRunHandle(run); err != nil {
		return FollowUpItem{}, FollowUpClaimRef{}, err
	}
	id, err := db.ParseUUID(itemID)
	if err != nil {
		return FollowUpItem{}, FollowUpClaimRef{}, err
	}
	rid, err := db.ParseUUID(run.RunID)
	if err != nil {
		return FollowUpItem{}, FollowUpClaimRef{}, err
	}
	row, err := s.q.ReclaimAssignedFollowUpQueueItem(ctx, dbsqlc.ReclaimAssignedFollowUpQueueItemParams{QueueItemID: id, ExecutionRunID: rid, ExecutionOwnerID: pgtype.Text{String: run.OwnerID, Valid: true}, ExecutionFencingToken: pgtype.Int8{Int64: run.FencingToken, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return FollowUpItem{}, FollowUpClaimRef{}, ErrInvalidReference
	}
	if err != nil {
		return FollowUpItem{}, FollowUpClaimRef{}, err
	}
	item := followFromRow(row)
	ref := FollowUpClaimRef{ItemID: item.ID, RunID: run.RunID, OwnerID: run.OwnerID, FencingToken: run.FencingToken}
	return item, ref, nil
}

func (s *PostgresStore) ApplyFollowUp(ctx context.Context, ref FollowUpClaimRef) error {
	if strings.TrimSpace(ref.RunID) == "" || strings.TrimSpace(ref.OwnerID) == "" || ref.FencingToken <= 0 {
		return errors.New("queue: owned claim reference is required")
	}
	id, err := db.ParseUUID(string(ref.ItemID))
	if err != nil {
		return err
	}
	rid, err := db.ParseUUID(ref.RunID)
	if err != nil {
		return err
	}
	_, err = s.q.ApplyFollowUpQueueItem(ctx, dbsqlc.ApplyFollowUpQueueItemParams{QueueItemID: id, ExecutionRunID: rid, ExecutionOwnerID: pgtype.Text{String: ref.OwnerID, Valid: true}, ExecutionFencingToken: pgtype.Int8{Int64: ref.FencingToken, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidReference
	}
	return err
}

// CreateContinuation is deterministic and idempotent: replaying the final
// boundary for the same follow-up item uses the same run and turn UUIDs.
func (s *PostgresStore) CreateContinuation(ctx context.Context, parent sessionruntime.RunHandle, item FollowUpItem) (string, error) {
	if strings.TrimSpace(parent.RunID) == "" || parent.FencingToken < 0 {
		return "", errors.New("queue: continuation parent run is required")
	}
	itemUUID, err := db.ParseUUID(string(item.ID))
	if err != nil {
		return "", err
	}
	parentUUID, err := db.ParseUUID(parent.RunID)
	if err != nil {
		return "", err
	}
	runUUID := uuid.NewSHA1(uuid.Nil, []byte("memoh.continuation.run:"+string(item.ID)))
	turnUUID := uuid.NewSHA1(uuid.Nil, []byte("memoh.continuation.turn:"+string(item.ID)))
	h := sha256.Sum256(item.Payload)
	row, err := s.q.CreateContinuationFromFollowUp(ctx, dbsqlc.CreateContinuationFromFollowUpParams{RunID: pgtype.UUID{Bytes: runUUID, Valid: true}, InvocationID: "continuation:" + string(item.ID), TurnID: pgtype.UUID{Bytes: turnUUID, Valid: true}, InputFingerprint: hex.EncodeToString(h[:]), ItemID: itemUUID, ParentRunID: parentUUID, ParentFencingToken: parent.FencingToken})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrContinuationLost
	}
	if err != nil {
		return "", err
	}
	return row.RunID.String(), nil
}

func steerFromRow(r dbsqlc.SessionSteerQueue) SteerItem {
	item := SteerItem{ID: SteerItemID(r.ItemID.String()), BotID: r.BotID.String(), SessionID: r.SessionID.String(), TargetRunID: r.TargetRunID.String(), Payload: append([]byte(nil), r.Payload...), Status: Status(r.Status), Position: r.Position, CreatedAt: r.CreatedAt.Time}
	if r.ClaimRunID.Valid && r.ClaimOwnerID.Valid && r.ClaimFencingToken.Valid {
		item.Claim = &SteerClaimRef{ItemID: item.ID, RunID: r.ClaimRunID.String(), OwnerID: r.ClaimOwnerID.String, FencingToken: r.ClaimFencingToken.Int64}
	}
	return item
}

func followFromRow(r dbsqlc.SessionFollowUpQueue) FollowUpItem {
	assigned := ""
	if r.AssignedRunID.Valid {
		assigned = r.AssignedRunID.String()
	}
	item := FollowUpItem{ID: FollowUpItemID(r.ItemID.String()), BotID: r.BotID.String(), SessionID: r.SessionID.String(), EnqueuedDuringRunID: r.EnqueuedDuringRunID.String(), AssignedRunID: assigned, Payload: append([]byte(nil), r.Payload...), Status: Status(r.Status), Position: r.Position, CreatedAt: r.CreatedAt.Time}
	if r.ClaimRunID.Valid && r.ClaimOwnerID.Valid && r.ClaimFencingToken.Valid {
		item.Claim = &FollowUpClaimRef{ItemID: item.ID, RunID: r.ClaimRunID.String(), OwnerID: r.ClaimOwnerID.String, FencingToken: r.ClaimFencingToken.Int64}
	}
	return item
}

func jsonPayloadEqual(left, right []byte) bool {
	decode := func(payload []byte) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		return value, nil
	}
	a, err := decode(left)
	if err != nil {
		return false
	}
	b, err := decode(right)
	return err == nil && reflect.DeepEqual(a, b)
}
