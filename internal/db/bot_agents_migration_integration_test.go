//go:build integration

package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/team"
)

func TestBotAgentsMigrationAndCanonicalSchema(t *testing.T) {
	t.Run("adds schema without backfilling legacy ACP data and reverses", func(t *testing.T) {
		ctx := context.Background()
		dsn := teamMigrationDSN(t)
		pool := freshMigratedDB(t)

		stepDown(t, dsn, 1)
		assertBotAgentsSchema(t, ctx, pool, false)

		const (
			userID      = "10000000-0000-4000-8000-000000000141"
			botID       = "20000000-0000-4000-8000-000000000141"
			nativeBotID = "30000000-0000-4000-8000-000000000141"
			sessionID   = "40000000-0000-4000-8000-000000000141"
			scheduleID  = "50000000-0000-4000-8000-000000000141"
		)

		if _, err := pool.Exec(ctx, `
			INSERT INTO public.users (id, username)
			VALUES ($1, 'bot-agents-migration-owner')`, userID); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO public.team_members (team_id, user_id)
			VALUES ($1, $2)`, team.DefaultTeamID, userID); err != nil {
			t.Fatalf("seed team membership: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO public.bots (
				id, team_id, owner_user_id, name, chat_runtime, chat_acp_agent_id, metadata
			)
			VALUES
				($1, $3, $4, 'bot-agents-migration-acp', 'acp_agent', 'codex',
				 '{"acp":{"agents":{"codex":{"enabled":true,"setup_mode":"self"}}}}'::jsonb),
				($2, $3, $4, 'bot-agents-migration-native', 'model', NULL, '{}'::jsonb)`,
			botID, nativeBotID, team.DefaultTeamID, userID,
		); err != nil {
			t.Fatalf("seed bots: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO public.bot_sessions (
				id, team_id, bot_id, type, session_mode, runtime_type, runtime_metadata, metadata
			)
			VALUES ($1, $2, $3, 'acp_agent', 'chat', 'acp_agent',
				'{"acp_agent_id":"codex","project_path":"/data"}'::jsonb,
				'{"acp_agent_id":"codex","project_path":"/data"}'::jsonb)`,
			sessionID, team.DefaultTeamID, botID,
		); err != nil {
			t.Fatalf("seed session: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO public.schedule (
				id, team_id, bot_id, name, description, pattern, command,
				run_target, runtime_type, acp_agent_id
			)
			VALUES ($1, $2, $3, 'migration schedule', '', '0 0 * * *', 'run',
				'new_session', 'acp_agent', 'codex')`,
			scheduleID, team.DefaultTeamID, botID,
		); err != nil {
			t.Fatalf("seed schedule: %v", err)
		}

		stepUp(t, dsn, 1)
		assertBotAgentsSchema(t, ctx, pool, true)
		assertBotAgentConstraintsValidated(t, ctx, pool, false)

		var agentRows int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.bot_agents`).Scan(&agentRows); err != nil {
			t.Fatalf("inspect Bot Agent rows: %v", err)
		}
		if agentRows != 0 {
			t.Fatalf("migration created %d Bot Agent rows, want schema-only migration", agentRows)
		}

		var defaultNull, sessionAgentNull, scheduleAgentNull bool
		if err := pool.QueryRow(ctx, `
			SELECT
				(SELECT default_bot_agent_id IS NULL FROM public.bots WHERE id = $1),
				(SELECT bot_agent_id IS NULL FROM public.bot_sessions WHERE id = $2),
				(SELECT bot_agent_id IS NULL FROM public.schedule WHERE id = $3)`,
			botID, sessionID, scheduleID,
		).Scan(&defaultNull, &sessionAgentNull, &scheduleAgentNull); err != nil {
			t.Fatalf("inspect legacy bindings: %v", err)
		}
		if !defaultNull || !sessionAgentNull || !scheduleAgentNull {
			t.Fatalf("migration populated bindings: default_null=%t session_null=%t schedule_null=%t", defaultNull, sessionAgentNull, scheduleAgentNull)
		}

		var nativeRows int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.bot_agents WHERE bot_id = $1`, nativeBotID).Scan(&nativeRows); err != nil {
			t.Fatalf("inspect Native rows: %v", err)
		}
		if nativeRows != 0 {
			t.Fatalf("Native bot created %d persisted Agent rows, want 0", nativeRows)
		}

		stepDown(t, dsn, 1)
		assertBotAgentsSchema(t, ctx, pool, false)
		stepUp(t, dsn, 1)
		assertBotAgentsSchema(t, ctx, pool, true)
		assertBotAgentConstraintsValidated(t, ctx, pool, false)
	})

	t.Run("canonical init contains final Bot Agent schema", func(t *testing.T) {
		ctx := context.Background()
		dsn := teamMigrationDSN(t)
		pool := resetToEmpty(t)
		applyCanonicalInitOnly(t, dsn)
		assertBotAgentsSchema(t, ctx, pool, true)
		assertBotAgentConstraintsValidated(t, ctx, pool, true)
	})
}

func assertBotAgentConstraintsValidated(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want bool) {
	t.Helper()
	var count int
	var allValidated bool
	if err := pool.QueryRow(ctx, `
		SELECT count(*), bool_and(convalidated)
		FROM pg_constraint
		WHERE conname IN (
			'bots_default_bot_agent_id_fkey',
			'bot_sessions_bot_agent_id_fkey',
			'schedule_bot_agent_id_fkey',
			'schedule_existing_session_check',
			'schedule_acp_fields_check'
		)
	`).Scan(&count, &allValidated); err != nil {
		t.Fatalf("inspect Bot Agent constraint validation: %v", err)
	}
	if count != 5 || allValidated != want {
		t.Fatalf("Bot Agent constraints: count=%d all_validated=%t, want count=5 all_validated=%t", count, allValidated, want)
	}
}

func assertBotAgentsSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want bool) {
	t.Helper()
	var table, botColumn, sessionColumn, scheduleColumn, rls, forceRLS bool
	var defaultFK, sessionFK, scheduleFK string
	if err := pool.QueryRow(ctx, `
		SELECT
			to_regclass('public.bot_agents') IS NOT NULL,
			EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'bots' AND column_name = 'default_bot_agent_id'),
			EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'bot_sessions' AND column_name = 'bot_agent_id'),
			EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'schedule' AND column_name = 'bot_agent_id'),
			COALESCE((SELECT relrowsecurity FROM pg_class WHERE oid = to_regclass('public.bot_agents')), false),
			COALESCE((SELECT relforcerowsecurity FROM pg_class WHERE oid = to_regclass('public.bot_agents')), false),
			COALESCE((SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = to_regclass('public.bots') AND conname = 'bots_default_bot_agent_id_fkey'), ''),
			COALESCE((SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = to_regclass('public.bot_sessions') AND conname = 'bot_sessions_bot_agent_id_fkey'), ''),
			COALESCE((SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = to_regclass('public.schedule') AND conname = 'schedule_bot_agent_id_fkey'), '')
	`).Scan(&table, &botColumn, &sessionColumn, &scheduleColumn, &rls, &forceRLS, &defaultFK, &sessionFK, &scheduleFK); err != nil {
		t.Fatalf("inspect Bot Agent schema: %v", err)
	}
	if !want {
		if table || botColumn || sessionColumn || scheduleColumn || defaultFK != "" || sessionFK != "" || scheduleFK != "" {
			t.Fatalf("Bot Agent schema survived down migration: table=%t bot=%t session=%t schedule=%t", table, botColumn, sessionColumn, scheduleColumn)
		}
		return
	}
	if !table || !botColumn || !sessionColumn || !scheduleColumn || !rls || !forceRLS {
		t.Fatalf("Bot Agent schema missing: table=%t bot=%t session=%t schedule=%t rls=%t force=%t", table, botColumn, sessionColumn, scheduleColumn, rls, forceRLS)
	}
	for name, definition := range map[string]string{
		"default":  defaultFK,
		"session":  sessionFK,
		"schedule": scheduleFK,
	} {
		if !strings.Contains(definition, "REFERENCES bot_agents(team_id, bot_id, id)") {
			t.Fatalf("%s Bot Agent FK = %q", name, definition)
		}
	}
}
