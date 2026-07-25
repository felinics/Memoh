package assembly

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	agentsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

// RouteSessionCoordinator preserves Agent session lock ordering while a
// Channel-owned route mutation runs in the same PostgreSQL transaction.
type RouteSessionCoordinator struct {
	pool *pgxpool.Pool
}

func NewRouteSessionCoordinator(pool *pgxpool.Pool) *RouteSessionCoordinator {
	return &RouteSessionCoordinator{pool: pool}
}

func (c *RouteSessionCoordinator) WithLockedRouteSessions(ctx context.Context, routeID string, mutate func(pgx.Tx) error) error {
	id, err := db.ParseUUID(routeID)
	if err != nil {
		return fmt.Errorf("invalid route id: %w", err)
	}
	return c.inTransaction(ctx, func(tx pgx.Tx, queries *agentsqlc.Queries) error {
		if _, err := queries.LockSessionsByRoute(ctx, id); err != nil {
			return err
		}
		return mutate(tx)
	})
}

func (c *RouteSessionCoordinator) WithLockedSession(ctx context.Context, sessionID string, mutate func(pgx.Tx) error) error {
	return c.inTransaction(ctx, func(tx pgx.Tx, queries *agentsqlc.Queries) error {
		if strings.TrimSpace(sessionID) != "" {
			id, err := db.ParseUUID(sessionID)
			if err != nil {
				return fmt.Errorf("invalid session id: %w", err)
			}
			if _, err := queries.LockSessionForRouteAssignment(ctx, id); err != nil {
				return err
			}
		}
		return mutate(tx)
	})
}

func (c *RouteSessionCoordinator) inTransaction(ctx context.Context, fn func(pgx.Tx, *agentsqlc.Queries) error) error {
	if c == nil || c.pool == nil {
		return errors.New("route session coordinator requires a postgres pool")
	}
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx, agentsqlc.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
