//go:build integration

package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/internal/team"
)

func TestBotAgentsMigrationAndCanonicalSchema(t *testing.T) {
	t.Run("backfills legacy ACP bindings and reverses", func(t *testing.T) {
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

		var agentID, provider string
		var enabled bool
		if err := pool.QueryRow(ctx, `
			SELECT id::text, enabled, metadata->>'provider'
			FROM public.bot_agents
			WHERE team_id = $1 AND bot_id = $2`,
			team.DefaultTeamID, botID,
		).Scan(&agentID, &enabled, &provider); err != nil {
			t.Fatalf("inspect backfilled Agent: %v", err)
		}
		if !enabled || provider != "codex" {
			t.Fatalf("backfilled Agent enabled=%t provider=%q", enabled, provider)
		}

		var defaultID, sessionAgentID, scheduleAgentID string
		if err := pool.QueryRow(ctx, `
			SELECT
				(SELECT default_bot_agent_id::text FROM public.bots WHERE id = $1),
				(SELECT bot_agent_id::text FROM public.bot_sessions WHERE id = $2),
				(SELECT bot_agent_id::text FROM public.schedule WHERE id = $3)`,
			botID, sessionID, scheduleID,
		).Scan(&defaultID, &sessionAgentID, &scheduleAgentID); err != nil {
			t.Fatalf("inspect backfilled bindings: %v", err)
		}
		if defaultID != agentID || sessionAgentID != agentID || scheduleAgentID != agentID {
			t.Fatalf("bindings default=%q session=%q schedule=%q, want %q", defaultID, sessionAgentID, scheduleAgentID, agentID)
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
	})

	t.Run("canonical init contains final Bot Agent schema", func(t *testing.T) {
		ctx := context.Background()
		dsn := teamMigrationDSN(t)
		pool := resetToEmpty(t)
		applyCanonicalInitOnly(t, dsn)
		assertBotAgentsSchema(t, ctx, pool, true)
	})
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
