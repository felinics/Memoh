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
	"fmt"
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

// EnqueueSteer admits one steer item for the session's active run. The
// caller owns the transaction; the statement itself locks the session row so
// concurrent admissions serialize and take contiguous positions. A replayed
// invocation returns the existing item when its payload matches and
// ErrInvocationConflict otherwise.
func (s *PostgresStore) EnqueueSteer(ctx context.Context, botID, sessionID, itemID, invocationID string, payload []byte) (SteerItem, error) {
	if s == nil || s.q == nil {
		return SteerItem{}, errors.New("queue: postgres store is not configured")
	}
	ids, err := parseEnqueueIDs(botID, sessionID, itemID, invocationID)
	if err != nil {
		return SteerItem{}, err
	}
	row, err := s.q.EnqueueSteerQueueItem(ctx, dbsqlc.EnqueueSteerQueueItemParams{
		ItemID: ids.item, BotID: ids.bot, SessionID: ids.session, InvocationID: ids.invocation, Payload: payload,
	})
	if err == nil {
		return steerFromRow(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return SteerItem{}, err
	}
	existing, lookupErr := s.q.GetSteerQueueItemByInvocation(ctx, dbsqlc.GetSteerQueueItemByInvocationParams{SessionID: ids.session, InvocationID: ids.invocation})
	if errors.Is(lookupErr, pgx.ErrNoRows) {
		return SteerItem{}, ErrNoActiveRun
	}
	if lookupErr != nil {
		return SteerItem{}, lookupErr
	}
	if !jsonPayloadEqual(existing.Payload, payload) {
		return SteerItem{}, ErrInvocationConflict
	}
	return steerFromRow(existing), nil
}

// EnqueueFollowUp admits one follow-up item. It shares EnqueueSteer's
// locking, replay, and no-active-run semantics while keeping the types apart.
func (s *PostgresStore) EnqueueFollowUp(ctx context.Context, botID, sessionID, itemID, invocationID string, payload []byte) (FollowUpItem, error) {
	if s == nil || s.q == nil {
		return FollowUpItem{}, errors.New("queue: postgres store is not configured")
	}
	ids, err := parseEnqueueIDs(botID, sessionID, itemID, invocationID)
	if err != nil {
		return FollowUpItem{}, err
	}
	row, err := s.q.EnqueueFollowUpQueueItem(ctx, dbsqlc.EnqueueFollowUpQueueItemParams{
		ItemID: ids.item, BotID: ids.bot, SessionID: ids.session, InvocationID: ids.invocation, Payload: payload,
	})
	if err == nil {
		return followFromRow(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return FollowUpItem{}, err
	}
	existing, lookupErr := s.q.GetFollowUpQueueItemByInvocation(ctx, dbsqlc.GetFollowUpQueueItemByInvocationParams{SessionID: ids.session, InvocationID: ids.invocation})
	if errors.Is(lookupErr, pgx.ErrNoRows) {
		return FollowUpItem{}, ErrNoActiveRun
	}
	if lookupErr != nil {
		return FollowUpItem{}, lookupErr
	}
	if !jsonPayloadEqual(existing.Payload, payload) {
		return FollowUpItem{}, ErrInvocationConflict
	}
	return followFromRow(existing), nil
}

type enqueueIDs struct {
	bot, session, item pgtype.UUID
	invocation         string
}

func parseEnqueueIDs(botID, sessionID, itemID, invocationID string) (enqueueIDs, error) {
	var ids enqueueIDs
	var err error
	if ids.bot, err = db.ParseUUID(botID); err != nil {
		return ids, err
	}
	if ids.session, err = db.ParseUUID(sessionID); err != nil {
		return ids, err
	}
	if ids.item, err = db.ParseUUID(itemID); err != nil {
		return ids, err
	}
	ids.invocation = strings.TrimSpace(invocationID)
	if ids.invocation == "" {
		return ids, ErrInvalidReference
	}
	return ids, nil
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

// DefaultPendingListLimit bounds each queue in the UI list response. Steers
// and follow-ups beyond it still exist and are consumed in order; the UI only
// renders the head of each queue.
const DefaultPendingListLimit = 200

// PendingQueues reads both pending queues in one statement snapshot and one
// round trip. The queue discriminator keeps the typed results separate; there
// is no mixed item type.
func (s *PostgresStore) PendingQueues(ctx context.Context, sessionID string, limit int) ([]SteerItem, []FollowUpItem, error) {
	sid, err := db.ParseUUID(sessionID)
	if err != nil {
		return nil, nil, err
	}
	if limit <= 0 {
		limit = DefaultPendingListLimit
	}
	rows, err := s.q.ListPendingSessionQueues(ctx, dbsqlc.ListPendingSessionQueuesParams{TargetSessionID: sid, MaxItems: int64(limit)})
	if err != nil {
		return nil, nil, err
	}
	steers := make([]SteerItem, 0, len(rows))
	followUps := make([]FollowUpItem, 0, len(rows))
	for _, row := range rows {
		switch row.Queue {
		case "steer":
			steers = append(steers, SteerItem{
				ID: SteerItemID(row.ItemID.String()), BotID: row.BotID.String(), SessionID: row.SessionID.String(),
				TargetRunID: row.RunID.String(), Payload: row.Payload, Status: Status(row.Status),
				Position: row.Position, CreatedAt: row.CreatedAt.Time,
			})
		case "follow_up":
			followUps = append(followUps, FollowUpItem{
				ID: FollowUpItemID(row.ItemID.String()), BotID: row.BotID.String(), SessionID: row.SessionID.String(),
				EnqueuedDuringRunID: row.RunID.String(), Payload: row.Payload, Status: Status(row.Status),
				Position: row.Position, CreatedAt: row.CreatedAt.Time,
			})
		default:
			return nil, nil, fmt.Errorf("queue: unknown pending queue discriminator %q", row.Queue)
		}
	}
	return steers, followUps, nil
}

// NextPendingFollowUp returns only the FIFO head. Final handoff uses this
// path so a large follow-up backlog is never copied into the coordinator.
func (s *PostgresStore) NextPendingFollowUp(ctx context.Context, sessionID string) (FollowUpItem, error) {
	sid, err := db.ParseUUID(sessionID)
	if err != nil {
		return FollowUpItem{}, err
	}
	row, err := s.q.GetNextPendingFollowUpQueueItem(ctx, sid)
	if errors.Is(err, pgx.ErrNoRows) {
		return FollowUpItem{}, ErrNotPending
	}
	if err != nil {
		return FollowUpItem{}, err
	}
	return followFromRow(row), nil
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
	id, err := db.ParseUUID(string(item.ItemID))
	if err != nil {
		return nil, err
	}
	var beforeID pgtype.UUID
	if before.ItemID != "" {
		beforeID, err = db.ParseUUID(string(before.ItemID))
		if err != nil {
			return nil, err
		}
	}
	rows, err := s.q.ReorderAcceptedSteerQueue(ctx, dbsqlc.ReorderAcceptedSteerQueueParams{SessionID: sid, ItemID: id, BeforeItemID: beforeID})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotPending
	}
	result := make([]SteerItem, 0, len(rows))
	for _, row := range rows {
		result = append(result, steerFromReorderRow(row))
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
	id, err := db.ParseUUID(string(item.ItemID))
	if err != nil {
		return nil, err
	}
	var beforeID pgtype.UUID
	if before.ItemID != "" {
		beforeID, err = db.ParseUUID(string(before.ItemID))
		if err != nil {
			return nil, err
		}
	}
	rows, err := s.q.ReorderAcceptedFollowUpQueue(ctx, dbsqlc.ReorderAcceptedFollowUpQueueParams{SessionID: sid, ItemID: id, BeforeItemID: beforeID})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotPending
	}
	result := make([]FollowUpItem, 0, len(rows))
	for _, row := range rows {
		result = append(result, followFromReorderRow(row))
	}
	return result, nil
}

func steerFromReorderRow(r dbsqlc.ReorderAcceptedSteerQueueRow) SteerItem {
	return steerFromRow(dbsqlc.SessionSteerQueue(r))
}

func followFromReorderRow(r dbsqlc.ReorderAcceptedFollowUpQueueRow) FollowUpItem {
	return followFromRow(dbsqlc.SessionFollowUpQueue(r))
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

// ClaimNextSteer claims the earliest accepted steer for a run in one SQL
// statement. CommitStep already owns the session reconciliation lock, so there
// is no need to list the whole queue and issue one claim statement per item.
func (s *PostgresStore) ClaimNextSteer(ctx context.Context, sessionID string, run sessionruntime.RunHandle) (SteerItem, error) {
	if err := validateRunHandle(run); err != nil {
		return SteerItem{}, err
	}
	sid, err := db.ParseUUID(sessionID)
	if err != nil {
		return SteerItem{}, err
	}
	rid, err := db.ParseUUID(run.RunID)
	if err != nil {
		return SteerItem{}, err
	}
	row, err := s.q.ClaimNextSteerQueueItem(ctx, dbsqlc.ClaimNextSteerQueueItemParams{
		SessionID: sid, ExecutionRunID: rid,
		ExecutionOwnerID:      pgtype.Text{String: run.OwnerID, Valid: true},
		ExecutionFencingToken: pgtype.Int8{Int64: run.FencingToken, Valid: true},
	})
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

// ApplySteerItem applies a claim and returns the row captured by the same CAS.
// Returning the row avoids a separate pre-validation SELECT in CommitStep.
func (s *PostgresStore) ApplySteerItem(ctx context.Context, ref SteerClaimRef) (SteerItem, error) {
	if strings.TrimSpace(ref.RunID) == "" || strings.TrimSpace(ref.OwnerID) == "" || ref.FencingToken <= 0 {
		return SteerItem{}, errors.New("queue: owned claim reference is required")
	}
	id, err := db.ParseUUID(string(ref.ItemID))
	if err != nil {
		return SteerItem{}, err
	}
	rid, err := db.ParseUUID(ref.RunID)
	if err != nil {
		return SteerItem{}, err
	}
	row, err := s.q.ApplySteerQueueItem(ctx, dbsqlc.ApplySteerQueueItemParams{QueueItemID: id, ExecutionRunID: rid, ExecutionOwnerID: pgtype.Text{String: ref.OwnerID, Valid: true}, ExecutionFencingToken: pgtype.Int8{Int64: ref.FencingToken, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return SteerItem{}, ErrInvalidReference
	}
	if err != nil {
		return SteerItem{}, err
	}
	return steerFromRow(row), nil
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
