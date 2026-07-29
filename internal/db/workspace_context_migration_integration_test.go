//go:build integration

package db_test

import (
	"context"
	"testing"
)

func TestWorkspaceContextMigrationRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := pool.Config().ConnString()

	assertWorkspaceContextTable := func(want bool) {
		t.Helper()
		var exists bool
		if err := pool.QueryRow(ctx, `
			SELECT to_regclass('public.bot_workspace_context_snapshots') IS NOT NULL
		`).Scan(&exists); err != nil {
			t.Fatalf("inspect workspace context table: %v", err)
		}
		if exists != want {
			t.Fatalf("workspace context table exists = %v, want %v", exists, want)
		}
	}

	assertWorkspaceContextTable(true)
	var targetKeyColumns []string
	if err := pool.QueryRow(ctx, `
		SELECT array_agg(attr.attname ORDER BY key.ord)
		  FROM pg_constraint con
		  JOIN LATERAL unnest(con.conkey) WITH ORDINALITY key(attnum, ord) ON true
		  JOIN pg_attribute attr ON attr.attrelid = con.conrelid AND attr.attnum = key.attnum
		 WHERE con.conrelid = 'public.bot_workspace_context_snapshots'::regclass
		   AND con.conname = 'bot_workspace_context_snapshots_target_key'
	`).Scan(&targetKeyColumns); err != nil {
		t.Fatalf("inspect workspace context target key: %v", err)
	}
	if len(targetKeyColumns) != 3 ||
		targetKeyColumns[0] != "team_id" ||
		targetKeyColumns[1] != "bot_id" ||
		targetKeyColumns[2] != "target_id" {
		t.Fatalf("workspace context target key = %v, want [team_id bot_id target_id]", targetKeyColumns)
	}

	stepDown(t, dsn, 1)
	assertWorkspaceContextTable(false)
	stepUp(t, dsn, 1)
	assertWorkspaceContextTable(true)
}
