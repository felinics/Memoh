// Package chat implements Chat-owned PostgreSQL persistence.
package chat

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	agentsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
)

var errBotSessionWriteLockerRequired = errors.New("bot session write locker factory is required")

// BotSessionWriteLocker is the consumer-owned port for api.bots row locks
// that serialize session/message writes (LockBotForSessionWrite).
type BotSessionWriteLocker interface {
	LockBotForSessionWrite(context.Context, pgtype.UUID) (pgtype.UUID, error)
}

// BotSessionWriteLockerFromTx binds a BotSessionWriteLocker to an open transaction.
// Composition must supply an API/Bot-owned implementation; Agent must not query api.bots.
type BotSessionWriteLockerFromTx func(tx pgx.Tx) BotSessionWriteLocker

type MessageStore struct {
	queries    *agentsqlc.Queries
	locker     BotSessionWriteLocker
	bindLocker BotSessionWriteLockerFromTx
	pool       *pgxpool.Pool
}

type ThreadStore struct {
	agentQueries *agentsqlc.Queries
	pool         *pgxpool.Pool
}

// NewMessageStore creates a store whose queries are already transaction-bound.
func NewMessageStore(locker BotSessionWriteLocker, db agentsqlc.DBTX) *MessageStore {
	return &MessageStore{queries: agentsqlc.New(db), locker: locker}
}

// NewMessageStoreWithPool creates a store that can open transactions.
// bindLocker must return a locker scoped to the same transaction as agent writes.
func NewMessageStoreWithPool(locker BotSessionWriteLocker, bindLocker BotSessionWriteLockerFromTx, pool *pgxpool.Pool) *MessageStore {
	return &MessageStore{
		queries:    agentsqlc.New(pool),
		locker:     locker,
		bindLocker: bindLocker,
		pool:       pool,
	}
}

// NewThreadStore creates a store whose queries are already transaction-bound.
func NewThreadStore(db agentsqlc.DBTX) *ThreadStore {
	return &ThreadStore{agentQueries: agentsqlc.New(db)}
}

// NewThreadStoreWithPool creates a store that can open transactions.
func NewThreadStoreWithPool(pool *pgxpool.Pool) *ThreadStore {
	return &ThreadStore{agentQueries: agentsqlc.New(pool), pool: pool}
}

func inThreadTransaction(ctx context.Context, pool *pgxpool.Pool, fn func(*agentsqlc.Queries) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	if err := fn(agentsqlc.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func inMessageTransaction(
	ctx context.Context,
	pool *pgxpool.Pool,
	bindLocker BotSessionWriteLockerFromTx,
	fn func(BotSessionWriteLocker, agentsqlc.DBTX) error,
) error {
	if bindLocker == nil {
		return errBotSessionWriteLockerRequired
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	if err := fn(bindLocker(tx), tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
