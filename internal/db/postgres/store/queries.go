package postgresstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	dbpkg "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type Queries struct {
	*dbsqlc.Queries
	pool *pgxpool.Pool
}

func NewQueries(queries *dbsqlc.Queries) *Queries {
	return &Queries{Queries: queries}
}

func NewQueriesWithPool(pool *pgxpool.Pool, queries *dbsqlc.Queries) *Queries {
	return &Queries{Queries: queries, pool: pool}
}

func (*Queries) SupportsAtomicDirectHistoryTurnWrites() bool {
	return true
}

// SupportsTransactions reports whether InTx opens a real PostgreSQL
// transaction. Wrappers without a pool or pinned connection retain the
// historical direct-execution fallback for tests and legacy callers.
func (q *Queries) SupportsTransactions() bool {
	return q != nil && q.pool != nil
}

func (q *Queries) WithTx(tx pgx.Tx) dbstore.Queries {
	if q == nil {
		return nil
	}
	return NewQueries(q.Queries.WithTx(tx))
}

func (q *Queries) InTx(ctx context.Context, fn func(dbstore.Queries) error) error {
	if q == nil || q.pool == nil {
		return fn(q)
	}
	var (
		tx  pgx.Tx
		err error
	)
	tx, err = q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	if err := fn(q.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return err
		}
		return fmt.Errorf("%w: %w", dbpkg.ErrCommitOutcomeUnknown, err)
	}
	return nil
}
