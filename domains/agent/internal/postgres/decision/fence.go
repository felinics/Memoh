package decision

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	runtimefence "github.com/memohai/memoh/domains/agent/chat/session/fence"
	agentsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"

	"github.com/memohai/memoh/internal/db"
)

func (s *fenceStore) InRuntimeFenceTransaction(ctx context.Context, fn func(runtimefence.Store) error) error {
	if fn == nil {
		return errors.New("runtime fence transaction callback is required")
	}
	return inTransaction(ctx, s.pool, func(tx pgx.Tx) error {
		return fn(&fenceStore{
			queries: s.queries.WithTx(tx),
			lock:    s.newLock(tx),
		})
	})
}

func (s *fenceStore) LockBot(ctx context.Context, botID string) error {
	return lockBot(ctx, s.lock, botID)
}

func (s *fenceStore) LockForActivation(ctx context.Context, fence runtimefence.Fence) (int64, error) {
	bot, session, err := fenceIDs(fence)
	if err != nil {
		return 0, err
	}
	value, err := s.queries.LockSessionRuntimeFenceForActivation(ctx, agentsqlc.LockSessionRuntimeFenceForActivationParams{
		BotID: bot, SessionID: session,
	})
	return value, mapFenceError(err)
}

func (s *fenceStore) Activate(ctx context.Context, fence runtimefence.Fence) (int64, error) {
	bot, session, err := fenceIDs(fence)
	if err != nil {
		return 0, err
	}
	value, err := s.queries.ActivateSessionRuntimeFence(ctx, agentsqlc.ActivateSessionRuntimeFenceParams{
		BotID: bot, SessionID: session, RuntimeFencingToken: fence.Token,
	})
	return value, mapFenceError(err)
}

func (s *fenceStore) ClaimToolApproval(ctx context.Context, id string, fence runtimefence.Fence) error {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	bot, session, err := fenceIDs(fence)
	if err != nil {
		return err
	}
	_, err = s.queries.ClaimToolApprovalRequestForRuntime(ctx, agentsqlc.ClaimToolApprovalRequestForRuntimeParams{
		ID: pgID, BotID: bot, SessionID: session,
		RuntimeFencingToken: optionalInt64(&fence.Token),
	})
	return mapFenceError(err)
}

func (s *fenceStore) ClaimUserInput(ctx context.Context, id string, fence runtimefence.Fence) error {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	bot, session, err := fenceIDs(fence)
	if err != nil {
		return err
	}
	_, err = s.queries.ClaimUserInputRequestForRuntime(ctx, agentsqlc.ClaimUserInputRequestForRuntimeParams{
		ID: pgID, BotID: bot, SessionID: session,
		RuntimeFencingToken: optionalInt64(&fence.Token),
	})
	return mapFenceError(err)
}

func (s *fenceStore) SupersedeToolApprovals(ctx context.Context, fence runtimefence.Fence, preserveID, reason string) error {
	bot, session, err := fenceIDs(fence)
	if err != nil {
		return err
	}
	_, err = s.queries.SupersedePendingToolApprovalsBySession(ctx, agentsqlc.SupersedePendingToolApprovalsBySessionParams{
		BotID: bot, SessionID: session, PreserveID: optionalUUID(preserveID), Reason: reason,
	})
	return err
}

func (s *fenceStore) SupersedeUserInputs(ctx context.Context, fence runtimefence.Fence, preserveID string, result []byte) error {
	bot, session, err := fenceIDs(fence)
	if err != nil {
		return err
	}
	_, err = s.queries.SupersedePendingUserInputsBySession(ctx, agentsqlc.SupersedePendingUserInputsBySessionParams{
		BotID: bot, SessionID: session, PreserveID: optionalUUID(preserveID), ResultJson: result,
	})
	return err
}

func (s *fenceStore) Lock(ctx context.Context, fence runtimefence.Fence) error {
	return lockFence(ctx, s.queries, fence)
}

func lockFence(ctx context.Context, queries *agentsqlc.Queries, fence runtimefence.Fence) error {
	bot, session, err := fenceIDs(fence)
	if err != nil {
		return err
	}
	_, err = queries.LockSessionRuntimeFence(ctx, agentsqlc.LockSessionRuntimeFenceParams{
		BotID: bot, SessionID: session, RuntimeFencingToken: fence.Token,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return runtimefence.ErrStale
	}
	return err
}

func fenceIDs(fence runtimefence.Fence) (pgtype.UUID, pgtype.UUID, error) {
	bot, err := db.ParseUUID(fence.BotID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	session, err := db.ParseUUID(fence.SessionID)
	return bot, session, err
}
