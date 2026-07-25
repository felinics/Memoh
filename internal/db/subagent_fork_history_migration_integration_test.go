//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSubagentForkHistoryMigrationRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := teamMigrationDSN(t)

	steps := countMigrationsFrom(t, "0119_subagent_fork_history.up.sql")

	assertSubagentForkHistorySchema(t, ctx, pool, false, true)
	stepDown(t, dsn, steps)
	assertSubagentForkHistorySchema(t, ctx, pool, true, false)
	stepUp(t, dsn, steps)
	assertSubagentForkHistorySchema(t, ctx, pool, false, true)
}

func assertSubagentForkHistorySchema(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	wantParentMessages bool,
	wantContextIndex bool,
) {
	t.Helper()
	var hasParentMessages, hasContextIndex bool
	if err := pool.QueryRow(ctx, `
		SELECT
		  EXISTS (
		    SELECT 1
		    FROM information_schema.columns
		    WHERE table_schema = 'public'
		      AND table_name = 'subagent_configs'
		      AND column_name = 'parent_messages'
		  ),
		  to_regclass('public.idx_bot_history_messages_subagent_fork_context') IS NOT NULL
	`).Scan(&hasParentMessages, &hasContextIndex); err != nil {
		t.Fatalf("inspect subagent fork history schema: %v", err)
	}
	if hasParentMessages != wantParentMessages || hasContextIndex != wantContextIndex {
		t.Fatalf(
			"subagent fork history schema = parent_messages:%t index:%t, want parent_messages:%t index:%t",
			hasParentMessages,
			hasContextIndex,
			wantParentMessages,
			wantContextIndex,
		)
	}
}
