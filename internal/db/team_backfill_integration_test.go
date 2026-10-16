//go:build integration

package db_test

import (
	"context"
	"testing"
)

func TestTeamIDBackfilledOnFreshInstall(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)

	// Enumerate applied team tables.
	rows, err := pool.Query(ctx, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
		  AND table_name NOT IN ('schema_migrations', 'teams', 'users')
		ORDER BY table_name`)
	if err != nil {
		t.Fatalf("enumerate team tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		tables = append(tables, n)
	}
	rows.Close()
	if len(tables) == 0 {
		t.Fatal("expected a non-empty set of team tables")
	}

	// Every team table must now have a team_id column, and it must be fully
	// backfilled (zero NULLs) to DefaultTeamID.
	for _, tbl := range tables {
		var hasCol bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM information_schema.columns
				WHERE table_schema='public' AND table_name=$1 AND column_name='team_id')`, tbl,
		).Scan(&hasCol); err != nil {
			t.Fatalf("check team_id on %s: %v", tbl, err)
		}
		if !hasCol {
			t.Fatalf("team table %q is missing a team_id column", tbl)
		}

		var nulls int
		if err := pool.QueryRow(ctx,
			"SELECT count(*) FROM "+quoteIdent(tbl)+" WHERE team_id IS NULL",
		).Scan(&nulls); err != nil {
			t.Fatalf("count NULL team_id on %s: %v", tbl, err)
		}
		if nulls != 0 {
			t.Fatalf("team table %q has %d rows with NULL team_id after backfill", tbl, nulls)
		}
	}
}

// quoteIdent double-quotes a SQL identifier for safe interpolation of a table
// name that comes from information_schema (not user input).
func quoteIdent(id string) string {
	out := make([]byte, 0, len(id)+2)
	out = append(out, '"')
	for i := 0; i < len(id); i++ {
		if id[i] == '"' {
			out = append(out, '"')
		}
		out = append(out, id[i])
	}
	out = append(out, '"')
	return string(out)
}
