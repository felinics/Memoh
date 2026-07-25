// Package postgres implements Network-owned PostgreSQL persistence.
package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/runtime/internal/postgres/sqlc"
	"github.com/memohai/memoh/domains/runtime/network"
	"github.com/memohai/memoh/internal/db"
)

type networkQueries interface {
	GetContainerByBotID(context.Context, pgtype.UUID) (sqlc.RuntimeContainer, error)
}

// Store adapts generated Runtime workspace statements to Network's narrow
// Runtime-owned reader.
type Store struct {
	queries networkQueries
}

var _ network.WorkspaceReader = (*Store)(nil)

// NewStore creates a postgres-backed workspace reader from a pool.
func NewStore(pool *pgxpool.Pool) *Store {
	if pool == nil {
		return nil
	}
	return &Store{queries: sqlc.New(pool)}
}

// NewStoreWithQueries injects a query surface for tests.
func NewStoreWithQueries(queries networkQueries) *Store {
	return &Store{queries: queries}
}

func (s *Store) GetWorkspaceContainer(ctx context.Context, botID string) (network.WorkspaceContainer, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return network.WorkspaceContainer{}, err
	}
	row, err := s.queries.GetContainerByBotID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return network.WorkspaceContainer{}, network.ErrWorkspaceContainerMissing
	}
	if err != nil {
		return network.WorkspaceContainer{}, err
	}
	return network.WorkspaceContainer{ContainerID: row.ContainerID}, nil
}
