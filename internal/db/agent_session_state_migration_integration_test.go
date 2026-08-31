//go:build integration

package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/team"
)

func TestAgentSessionStateMigrationAndCanonicalSchema(t *testing.T) {
	t.Run("migration chain is reversible", func(t *testing.T) {
		ctx := context.Background()
		dsn := teamMigrationDSN(t)
		pool := freshMigratedDB(t)

		assertAgentSessionStateSchema(t, ctx, pool, "agent")
		assertSessionRunCandidateIndex(t, ctx, pool, true, true)

		// 0145 renames the ACP-scoped checkpoint tables to agent_session_*;
		// rolling below it must restore the acp_* names with all structures.
		migrateTo(t, dsn, 144)
		assertAgentSessionStateSchema(t, ctx, pool, "acp")

		// Rolling below 0138 removes the checkpoint tables and detaches the
		// constraint while preserving 0137's standalone index.
		migrateTo(t, dsn, 137)
		assertAgentSessionStateSchema(t, ctx, pool, "")
		assertSessionRunCandidateIndex(t, ctx, pool, true, false)

		// 0137 is deliberately a single concurrent-index statement and is
		// independently reversible.
		stepDown(t, dsn, 1)
		assertSessionRunCandidateIndex(t, ctx, pool, false, false)
		stepUp(t, dsn, 1)
		assertSessionRunCandidateIndex(t, ctx, pool, true, false)

		migrateTo(t, dsn, 144)
		assertAgentSessionStateSchema(t, ctx, pool, "acp")
		migrateUpAll(t, dsn)
		assertAgentSessionStateSchema(t, ctx, pool, "agent")
		assertSessionRunCandidateIndex(t, ctx, pool, true, true)
	})

	t.Run("canonical init contains final Agent state schema", func(t *testing.T) {
		ctx := context.Background()
		dsn := teamMigrationDSN(t)
		pool := resetToEmpty(t)
		applyCanonicalInitOnly(t, dsn)
		assertAgentSessionStateSchema(t, ctx, pool, "agent")
		assertSessionRunCandidateIndex(t, ctx, pool, true, true)
	})

	t.Run("turn marker rewrite is reversible", func(t *testing.T) {
		ctx := context.Background()
		dsn := teamMigrationDSN(t)
		pool := freshMigratedDB(t)
		migrateTo(t, dsn, 145)

		const (
			userID    = "10000000-0000-4000-8000-000000000146"
			botID     = "20000000-0000-4000-8000-000000000146"
			sessionID = "30000000-0000-4000-8000-000000000146"
			messageID = "40000000-0000-4000-8000-000000000146"
		)
		seed := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO users (id, username) VALUES ($1, 'agent-turn-marker-owner')`, []any{userID}},
			{`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, []any{team.DefaultTeamID, userID}},
			{`INSERT INTO bots (id, team_id, owner_user_id, name, metadata)
			  VALUES ($1, $2, $3, 'agent-turn-marker-bot', '{}'::jsonb)`, []any{botID, team.DefaultTeamID, userID}},
			{`INSERT INTO bot_sessions (id, team_id, bot_id, type, session_mode, runtime_type)
			  VALUES ($1, $2, $3, 'chat', 'chat', 'model')`, []any{sessionID, team.DefaultTeamID, botID}},
			{`INSERT INTO bot_history_messages (
				id, team_id, bot_id, session_id, role, content, metadata
			  ) VALUES (
				$1, $2, $3, $4, 'assistant', '[]'::jsonb,
				'{"acp_turn_outcome":"succeeded","acp_decision_projection":true,"acp_decision_tool_call_id":"tool-1","keep":"value"}'::jsonb
			  )`, []any{messageID, team.DefaultTeamID, botID, sessionID}},
		}
		for _, statement := range seed {
			if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
				t.Fatalf("seed Agent turn marker migration: %v", err)
			}
		}

		stepUp(t, dsn, 1)
		assertTurnMarkerNames(t, ctx, pool, messageID, "agent", "acp")
		stepDown(t, dsn, 1)
		assertTurnMarkerNames(t, ctx, pool, messageID, "acp", "agent")
		migrateUpAll(t, dsn)
	})
}

func assertTurnMarkerNames(t *testing.T, ctx context.Context, pool *pgxpool.Pool, messageID, wantPrefix, absentPrefix string) {
	t.Helper()
	var outcome, toolCallID, untouched string
	var projection, hasOldKeys bool
	if err := pool.QueryRow(ctx, `
		SELECT
			metadata->>($2 || '_turn_outcome'),
			(metadata->>($2 || '_decision_projection'))::boolean,
			metadata->>($2 || '_decision_tool_call_id'),
			metadata->>'keep',
			metadata ?| ARRAY[
				$3 || '_turn_outcome',
				$3 || '_decision_projection',
				$3 || '_decision_tool_call_id'
			]
		FROM bot_history_messages
		WHERE id = $1`,
		messageID, wantPrefix, absentPrefix,
	).Scan(&outcome, &projection, &toolCallID, &untouched, &hasOldKeys); err != nil {
		t.Fatalf("inspect %s turn markers: %v", wantPrefix, err)
	}
	if outcome != "succeeded" || !projection || toolCallID != "tool-1" || untouched != "value" || hasOldKeys {
		t.Fatalf("%s turn markers = outcome=%q projection=%t tool=%q keep=%q old_keys=%t", wantPrefix, outcome, projection, toolCallID, untouched, hasOldKeys)
	}
}

func assertSessionRunCandidateIndex(
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
		t.Fatalf("inspect session run candidate index: %v", err)
	}
	if indexExists != wantIndex || constraintOwnsIndex != wantConstraintOwner {
		t.Fatalf(
			"session run candidate index: exists=%t constraint_owner=%t, want exists=%t constraint_owner=%t",
			indexExists,
			constraintOwnsIndex,
			wantIndex,
			wantConstraintOwner,
		)
	}
}

// assertAgentSessionStateSchema verifies the checkpoint tables exist under
// exactly one naming generation. wantPrefix is "agent" (post-0145), "acp"
// (pre-0145), or "" (tables absent entirely).
func assertAgentSessionStateSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wantPrefix string) {
	t.Helper()

	type generation struct {
		states, lines, publications, sessionIDColumn bool
		runFK, linesFK, publicationRunFK             string
		staleConstraintNames                         int
	}
	inspect := func(prefix, sessionIDColumn string) generation {
		var gen generation
		stalePrefix := "agent"
		if prefix == "agent" {
			stalePrefix = "acp"
		}
		if err := pool.QueryRow(ctx, `
			SELECT
				to_regclass('public.`+prefix+`_session_states') IS NOT NULL,
				to_regclass('public.`+prefix+`_session_state_lines') IS NOT NULL,
				to_regclass('public.`+prefix+`_session_publications') IS NOT NULL,
				EXISTS (
					SELECT 1 FROM information_schema.columns
					WHERE table_schema = 'public'
					  AND table_name = '`+prefix+`_session_states'
					  AND column_name = '`+sessionIDColumn+`'
				),
				COALESCE((
					SELECT pg_get_constraintdef(oid) FROM pg_constraint
					WHERE conrelid = to_regclass('public.`+prefix+`_session_states')
					  AND conname = '`+prefix+`_session_states_run_fkey'
				), ''),
				COALESCE((
					SELECT pg_get_constraintdef(oid) FROM pg_constraint
					WHERE conrelid = to_regclass('public.`+prefix+`_session_state_lines')
					  AND conname = '`+prefix+`_session_state_lines_session_fkey'
				), ''),
				COALESCE((
					SELECT pg_get_constraintdef(oid) FROM pg_constraint
					WHERE conrelid = to_regclass('public.`+prefix+`_session_publications')
						  AND conname = '`+prefix+`_session_publications_run_fkey'
					), ''),
					(
						SELECT count(*) FROM pg_constraint
						WHERE conrelid IN (
							to_regclass('public.`+prefix+`_session_states'),
							to_regclass('public.`+prefix+`_session_state_lines'),
							to_regclass('public.`+prefix+`_session_publications')
						)
						  AND left(conname, length('`+stalePrefix+`_session_')) = '`+stalePrefix+`_session_'
					)
		`).Scan(
			&gen.states, &gen.lines, &gen.publications, &gen.sessionIDColumn,
			&gen.runFK, &gen.linesFK, &gen.publicationRunFK, &gen.staleConstraintNames,
		); err != nil {
			t.Fatalf("inspect %s state schema: %v", prefix, err)
		}
		return gen
	}
	assertAbsent := func(prefix string, gen generation) {
		if gen.states || gen.lines || gen.publications || gen.runFK != "" || gen.linesFK != "" || gen.publicationRunFK != "" {
			t.Fatalf("%s_session_* schema unexpectedly present: %+v", prefix, gen)
		}
	}
	assertPresent := func(prefix string, gen generation) {
		if !gen.states || !gen.lines || !gen.publications || !gen.sessionIDColumn {
			t.Fatalf("%s_session_* schema missing relations: %+v", prefix, gen)
		}
		if gen.staleConstraintNames != 0 {
			t.Fatalf("%s_session_* schema retained %d constraints from the other naming generation", prefix, gen.staleConstraintNames)
		}
		if !strings.Contains(gen.runFK, "FOREIGN KEY (team_id, session_id, through_run_id)") ||
			!strings.Contains(gen.runFK, "REFERENCES session_runs(team_id, session_id, run_id) ON DELETE CASCADE") {
			t.Fatalf("%s state run FK = %q", prefix, gen.runFK)
		}
		if !strings.Contains(gen.linesFK, "REFERENCES bot_sessions(team_id, id) ON DELETE CASCADE") {
			t.Fatalf("%s state lines FK = %q", prefix, gen.linesFK)
		}
		if !strings.Contains(gen.publicationRunFK, "REFERENCES session_runs(team_id, session_id, run_id) ON DELETE CASCADE") {
			t.Fatalf("%s publication run FK = %q", prefix, gen.publicationRunFK)
		}
	}

	agentGen := inspect("agent", "agent_session_id")
	acpGen := inspect("acp", "acp_session_id")
	var candidateKey string
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE((
			SELECT pg_get_constraintdef(oid) FROM pg_constraint
			WHERE conrelid = to_regclass('public.session_runs')
			  AND conname = 'session_runs_team_session_run_key'
		), '')
	`).Scan(&candidateKey); err != nil {
		t.Fatalf("inspect session run candidate key: %v", err)
	}

	switch wantPrefix {
	case "agent":
		assertPresent("agent", agentGen)
		assertAbsent("acp", acpGen)
	case "acp":
		assertPresent("acp", acpGen)
		assertAbsent("agent", agentGen)
	case "":
		assertAbsent("agent", agentGen)
		assertAbsent("acp", acpGen)
		return
	default:
		t.Fatalf("unknown schema generation %q", wantPrefix)
	}
	if candidateKey != "UNIQUE (team_id, session_id, run_id)" {
		t.Fatalf("session run candidate key = %q", candidateKey)
	}
}
