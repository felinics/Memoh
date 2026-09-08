package postgresstore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func (q *Queries) ClearHistoryByBot(ctx context.Context, id pgtype.UUID) error {
	return q.withHistoryCleanup(ctx, id, func(tx *sqlc.Queries) error {
		return tx.ClearHistoryByBot(ctx, id)
	})
}

func (q *Queries) ClearHistoryBySession(ctx context.Context, id pgtype.UUID) error {
	return q.withSessionHistoryCleanup(ctx, id, func(tx *sqlc.Queries) error {
		return tx.ClearHistoryBySession(ctx, id)
	})
}

func (q *Queries) SoftDeleteSession(ctx context.Context, id pgtype.UUID) error {
	return q.withSessionHistoryCleanup(ctx, id, func(tx *sqlc.Queries) error {
		return tx.SoftDeleteSession(ctx, id)
	})
}

func (q *Queries) SoftDeleteSessionsByBot(ctx context.Context, id pgtype.UUID) error {
	return q.withHistoryCleanup(ctx, id, func(tx *sqlc.Queries) error {
		return tx.SoftDeleteSessionsByBot(ctx, id)
	})
}

func (q *Queries) withSessionHistoryCleanup(ctx context.Context, id pgtype.UUID, fn func(*sqlc.Queries) error) error {
	session, err := q.GetSessionByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return q.withHistoryCleanup(ctx, session.BotID, fn)
}

func (q *Queries) withHistoryCleanup(ctx context.Context, botID pgtype.UUID, fn func(*sqlc.Queries) error) error {
	return q.InTx(ctx, func(tx dbstore.Queries) error {
		if _, err := tx.LockBotForRuntimeReset(ctx, botID); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		return fn(tx.(*Queries).Queries)
	})
}
