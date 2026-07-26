package bot

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	apisqlc "github.com/memohai/memoh/domains/api/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

// SessionLocker serializes Agent session writes against an API-owned bot.
type SessionLocker struct {
	queries *apisqlc.Queries
}

// NewSessionLocker constructs a pool-bound bot session locker.
func NewSessionLocker(pool *pgxpool.Pool) *SessionLocker {
	return &SessionLocker{queries: apisqlc.New(pool)}
}

// NewSessionLockerFromTx constructs a transaction-bound bot session locker.
func NewSessionLockerFromTx(tx pgx.Tx) *SessionLocker {
	return &SessionLocker{queries: apisqlc.New(tx)}
}

func (l *SessionLocker) LockBotForSessionWrite(ctx context.Context, id pgtype.UUID) (pgtype.UUID, error) {
	return l.queries.LockBotForSessionWrite(ctx, id)
}

// ExclusiveLocker serializes whole-bot operations in their caller's transaction.
type ExclusiveLocker struct{}

func (ExclusiveLocker) LockBotExclusive(ctx context.Context, tx pgx.Tx, botID string) error {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	_, err = apisqlc.New(tx).LockBotExclusive(ctx, id)
	return err
}
