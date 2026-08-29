//go:build integration

package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestACPSessionStateMigrationAndCanonicalSchema(t *testing.T) {
	t.Run("migration chain is reversible", func(t *testing.T) {
		ctx := context.Background()
		dsn := teamMigrationDSN(t)
		pool := freshMigratedDB(t)

		assertACPSessionStateSchema(t, ctx, pool, true)
		assertACPSessionRunCandidateIndex(t, ctx, pool, true, true)

		// 0142 changes the workdir FK, 0141 adds Bot Agents, 0140 removes
		// Heartbeat, and 0139 is the reset fence. Crossing 0138 removes the ACP
		// tables and detaches the constraint while preserving 0137's standalone
		// index.
		stepDown(t, dsn, 5)
		assertACPSessionStateSchema(t, ctx, pool, false)
		assertACPSessionRunCandidateIndex(t, ctx, pool, true, false)

		// 0137 is deliberately a single concurrent-index statement and is
		// independently reversible.
		stepDown(t, dsn, 1)
		assertACPSessionRunCandidateIndex(t, ctx, pool, false, false)
		stepUp(t, dsn, 1)
		assertACPSessionRunCandidateIndex(t, ctx, pool, true, false)

		stepUp(t, dsn, 5)
		assertACPSessionStateSchema(t, ctx, pool, true)
		assertACPSessionRunCandidateIndex(t, ctx, pool, true, true)
	})

	t.Run("canonical init contains final ACP state schema", func(t *testing.T) {
		ctx := context.Background()
		dsn := teamMigrationDSN(t)
		pool := resetToEmpty(t)
		applyCanonicalInitOnly(t, dsn)
		assertACPSessionStateSchema(t, ctx, pool, true)
		assertACPSessionRunCandidateIndex(t, ctx, pool, true, true)
	})
}

func assertACPSessionRunCandidateIndex(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	wantIndex bool,
	wantConstraintOwner bool,
) {
	t.Helper()
	var indexExists, constraintOwnsIndex bool
	if err := pool.QueryRow(ctx, `
		SELECT
			to_regclass('public.session_runs_team_session_run_key') IS NOT NULL,
			EXISTS (
				SELECT 1
				FROM pg_constraint
				WHERE conrelid = to_regclass('public.session_runs')
				  AND conname = 'session_runs_team_session_run_key'
				  AND conindid = to_regclass('public.session_runs_team_session_run_key')
			)
	`).Scan(&indexExists, &constraintOwnsIndex); err != nil {
		t.Fatalf("inspect ACP session run candidate index: %v", err)
	}
	if indexExists != wantIndex || constraintOwnsIndex != wantConstraintOwner {
		t.Fatalf(
			"ACP session run candidate index: exists=%t constraint_owner=%t, want exists=%t constraint_owner=%t",
			indexExists,
			constraintOwnsIndex,
			wantIndex,
			wantConstraintOwner,
		)
	}
}

func assertACPSessionStateSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want bool) {
	t.Helper()
	var states, lines, publications, shapesColumn bool
	var candidateKey, runFK, linesFK, publicationRunFK string
	if err := pool.QueryRow(ctx, `
		SELECT
			to_regclass('public.acp_session_states') IS NOT NULL,
			to_regclass('public.acp_session_state_lines') IS NOT NULL,
			to_regclass('public.acp_session_publications') IS NOT NULL,
			EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'acp_session_states'
				  AND column_name = 'file_shapes'
			),
			COALESCE((
				SELECT pg_get_constraintdef(oid) FROM pg_constraint
				WHERE conrelid = to_regclass('public.session_runs')
				  AND conname = 'session_runs_team_session_run_key'
			), ''),
			COALESCE((
				SELECT pg_get_constraintdef(oid) FROM pg_constraint
				WHERE conrelid = to_regclass('public.acp_session_states')
				  AND conname = 'acp_session_states_run_fkey'
			), ''),
			COALESCE((
				SELECT pg_get_constraintdef(oid) FROM pg_constraint
				WHERE conrelid = to_regclass('public.acp_session_state_lines')
				  AND conname = 'acp_session_state_lines_session_fkey'
			), ''),
			COALESCE((
				SELECT pg_get_constraintdef(oid) FROM pg_constraint
				WHERE conrelid = to_regclass('public.acp_session_publications')
				  AND conname = 'acp_session_publications_run_fkey'
			), '')
	`).Scan(&states, &lines, &publications, &shapesColumn, &candidateKey, &runFK, &linesFK, &publicationRunFK); err != nil {
		t.Fatalf("inspect ACP state schema: %v", err)
	}
	if !want {
		if states || lines || publications || candidateKey != "" || runFK != "" || linesFK != "" || publicationRunFK != "" {
			t.Fatalf("ACP schema survived 0138 down: states=%t lines=%t publications=%t candidate=%q runFK=%q linesFK=%q publicationFK=%q",
				states, lines, publications, candidateKey, runFK, linesFK, publicationRunFK)
		}
		return
	}
	if !states || !lines || !publications || !shapesColumn {
		t.Fatalf("ACP schema missing relations: states=%t lines=%t publications=%t file_shapes=%t", states, lines, publications, shapesColumn)
	}
	if candidateKey != "UNIQUE (team_id, session_id, run_id)" {
		t.Fatalf("session run candidate key = %q", candidateKey)
	}
	if !strings.Contains(runFK, "FOREIGN KEY (team_id, session_id, through_run_id)") ||
		!strings.Contains(runFK, "REFERENCES session_runs(team_id, session_id, run_id) ON DELETE CASCADE") {
		t.Fatalf("ACP state run FK = %q", runFK)
	}
	if !strings.Contains(linesFK, "REFERENCES bot_sessions(team_id, id) ON DELETE CASCADE") {
		t.Fatalf("ACP state lines FK = %q", linesFK)
	}
	if !strings.Contains(publicationRunFK, "REFERENCES session_runs(team_id, session_id, run_id) ON DELETE CASCADE") {
		t.Fatalf("ACP publication run FK = %q", publicationRunFK)
	}
}
