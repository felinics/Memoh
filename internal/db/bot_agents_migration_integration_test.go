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
		assertDirectScheduleRequiresBotAgent(t, ctx, pool, true)

		// Cross everything from 0141 up so later migrations (0142 Agent
		// credentials, ...) never silently shrink the descent.
		stepDown(t, dsn, countMigrationsFrom(t, "0141_bot_agents.up.sql"))
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
		// Return to head so later suites see a fully migrated database.
		migrateUpAll(t, dsn)
	})

	t.Run("retires removed built-in ACP agents without materializing direct agents", func(t *testing.T) {
		ctx := context.Background()
		dsn := teamMigrationDSN(t)
		pool := freshMigratedDB(t)
		migrateTo(t, dsn, 143)
		// 0141 intentionally left this historical CHECK unvalidated. Recreate
		// that real upgrade state; rolling down from today's canonical schema
		// would otherwise leave it validated and hide legacy-row failures.
		if _, err := pool.Exec(ctx, `ALTER TABLE schedule DROP CONSTRAINT schedule_acp_fields_check`); err != nil {
			t.Fatalf("drop schedule ACP fields constraint: %v", err)
		}

		const (
			userID         = "10000000-0000-4000-8000-000000000143"
			botID          = "20000000-0000-4000-8000-000000000143"
			agentID        = "30000000-0000-4000-8000-000000000143"
			credentialID   = "40000000-0000-4000-8000-000000000143"
			sessionID      = "50000000-0000-4000-8000-000000000143"
			scheduleID     = "60000000-0000-4000-8000-000000000143"
			sessionBotID   = "70000000-0000-4000-8000-000000000143"
			orphanSession  = "80000000-0000-4000-8000-000000000143"
			disabledBotID  = "90000000-0000-4000-8000-000000000143"
			disabledAgent  = "a0000000-0000-4000-8000-000000000143"
			disabledSched  = "b0000000-0000-4000-8000-000000000143"
			collisionBot   = "c0000000-0000-4000-8000-000000000143"
			legacySched    = "d0000000-0000-4000-8000-000000000143"
			disabledSess   = "e0000000-0000-4000-8000-000000000143"
			providerID     = "f0000000-0000-4000-8000-000000000143"
			modelID        = "f1000000-0000-4000-8000-000000000143"
			customAgent    = "f7000000-0000-4000-8000-000000000143"
			customSession  = "f8000000-0000-4000-8000-000000000143"
			customSchedule = "f9000000-0000-4000-8000-000000000143"
		)

		seed := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO users (id, username) VALUES ($1, 'external-agent-migration-owner')`, []any{userID}},
			{`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, []any{team.DefaultTeamID, userID}},
			{`INSERT INTO bots (
				id, team_id, owner_user_id, name, chat_runtime, chat_acp_agent_id, metadata
			) VALUES (
				$1, $2, $3, 'external-agent-migration-bot', 'acp_agent', 'codex',
				'{"acp":{"agents":{"codex":{"enabled":true,"setup_mode":"api_key","managed":{"api_key":"plaintext-must-not-move","base_url":"https://gateway.example/v1"}}}}}'::jsonb
			)`, []any{botID, team.DefaultTeamID, userID}},
			{`INSERT INTO bots (
				id, team_id, owner_user_id, name, chat_runtime, metadata
			) VALUES (
				$1, $2, $3, 'session-only-external-agent-migration-bot', 'model',
				'{"acp":{"agents":{"claude-code":{"enabled":true,"setup_mode":"self"}}}}'::jsonb
			)`, []any{sessionBotID, team.DefaultTeamID, userID}},
			{`INSERT INTO bots (
				id, team_id, owner_user_id, name, chat_runtime, chat_acp_agent_id, metadata
			) VALUES (
				$1, $2, $3, 'disabled-only-external-agent-migration-bot', 'acp_agent', 'codex',
				'{"acp":{"agents":{"codex":{"enabled":true,"setup_mode":"self"}}}}'::jsonb
			)`, []any{disabledBotID, team.DefaultTeamID, userID}},
			{`INSERT INTO bots (
				id, team_id, owner_user_id, name, chat_runtime, chat_acp_agent_id, metadata
			) VALUES (
				$1, $2, $3, 'external-agent-name-collision-bot', 'acp_agent', 'codex',
				'{"acp":{"agents":{"codex":{"enabled":true,"setup_mode":"self"}}}}'::jsonb
			)`, []any{collisionBot, team.DefaultTeamID, userID}},
			{`INSERT INTO agent_credentials (
				id, team_id, owner_user_id, provider, auth_kind, label,
				encrypted_payload, encryption_nonce, key_version
			) VALUES (
				$1, $2, $3, 'openai', 'openai_api_key', 'API key',
				decode('01', 'hex'), decode(repeat('00', 12), 'hex'), 1
			)`, []any{credentialID, team.DefaultTeamID, userID}},
			{`INSERT INTO providers (id, team_id, name)
			  VALUES ($1, $2, 'legacy schedule provider')`, []any{providerID, team.DefaultTeamID}},
			{`INSERT INTO models (id, team_id, model_id, provider_id)
			  VALUES ($1, $2, 'legacy-schedule-model', $3)`, []any{modelID, team.DefaultTeamID, providerID}},
			{`INSERT INTO bot_agents (
				id, team_id, bot_id, name, runtime, enabled, metadata, agent_credential_id
			) VALUES ($1, $2, $3, 'Codex', 'acp', true, '{"provider":"codex"}'::jsonb, $4)`, []any{agentID, team.DefaultTeamID, botID, credentialID}},
			{`INSERT INTO bot_agents (
				id, team_id, bot_id, name, runtime, enabled, metadata
			) VALUES ($1, $2, $3, 'Codex', 'acp', false, '{"provider":"codex"}'::jsonb)`, []any{disabledAgent, team.DefaultTeamID, disabledBotID}},
			{`INSERT INTO bot_agents (team_id, bot_id, name, runtime, enabled, metadata)
			  VALUES
			    ($1, $2, 'Codex', 'acp', true, '{"provider":"acp"}'::jsonb),
			    ($1, $2, 'Codex (migrated)', 'acp', true, '{"provider":"acp"}'::jsonb)`, []any{team.DefaultTeamID, collisionBot}},
			{`INSERT INTO bot_agents (id, team_id, bot_id, name, runtime, enabled, metadata)
			  VALUES ($1, $2, $3, 'Custom ACP', 'acp', true, '{"provider":"acp"}'::jsonb)`, []any{customAgent, team.DefaultTeamID, collisionBot}},
			{`UPDATE bots SET default_bot_agent_id = $1 WHERE id = $2`, []any{agentID, botID}},
			{`INSERT INTO bot_sessions (
				id, team_id, bot_id, bot_agent_id, type, session_mode, runtime_type, runtime_metadata, metadata
			) VALUES (
				$1, $2, $3, $4, 'acp_agent', 'chat', 'acp_agent',
				'{"acp_agent_id":"codex","project_path":"/data"}'::jsonb,
				'{"acp_agent_id":"codex","project_path":"/data"}'::jsonb
			)`, []any{sessionID, team.DefaultTeamID, botID, agentID}},
			{`INSERT INTO bot_sessions (
				id, team_id, bot_id, type, session_mode, runtime_type, runtime_metadata, metadata
			) VALUES (
				$1, $2, $3, 'acp_agent', 'chat', 'acp_agent',
				'{"acp_agent_id":"claude-code","project_path":"/data"}'::jsonb,
				'{"acp_agent_id":"claude-code","project_path":"/data"}'::jsonb
			)`, []any{orphanSession, team.DefaultTeamID, sessionBotID}},
			{`INSERT INTO bot_sessions (
				id, team_id, bot_id, type, session_mode, runtime_type, runtime_metadata, metadata
			) VALUES (
				$1, $2, $3, 'acp_agent', 'chat', 'acp_agent',
				'{"acp_agent_id":"codex","project_path":"/data"}'::jsonb,
				'{"acp_agent_id":"codex","project_path":"/data"}'::jsonb
			)`, []any{disabledSess, team.DefaultTeamID, disabledBotID}},
			{`INSERT INTO bot_sessions (
				id, team_id, bot_id, bot_agent_id, type, session_mode, runtime_type, runtime_metadata, metadata
			) VALUES (
				$1, $2, $3, $4, 'acp_agent', 'chat', 'acp_agent',
				'{"acp_agent_id":"acp","project_path":"/data"}'::jsonb,
				'{"acp_agent_id":"acp","project_path":"/data"}'::jsonb
			)`, []any{customSession, team.DefaultTeamID, collisionBot, customAgent}},
			{`INSERT INTO schedule (
				id, team_id, bot_id, bot_agent_id, name, description, pattern, command,
				run_target, runtime_type, acp_agent_id
			) VALUES (
				$1, $2, $3, $4, 'migration schedule', '', '0 0 * * *', 'run',
				'new_session', 'acp_agent', 'codex'
			)`, []any{scheduleID, team.DefaultTeamID, botID, agentID}},
			{`INSERT INTO schedule (
				id, team_id, bot_id, name, description, pattern, command,
				run_target, runtime_type, acp_agent_id
			) VALUES (
				$1, $2, $3, 'disabled-only migration schedule', '', '0 0 * * *', 'run',
				'new_session', 'acp_agent', 'codex'
			)`, []any{disabledSched, team.DefaultTeamID, disabledBotID}},
			{`INSERT INTO schedule (
				id, team_id, bot_id, bot_agent_id, name, description, pattern, command,
				run_target, runtime_type, acp_agent_id
			) VALUES (
				$1, $2, $3, $4, 'custom ACP schedule', '', '0 0 * * *', 'run',
				'new_session', 'acp_agent', 'acp'
			)`, []any{customSchedule, team.DefaultTeamID, collisionBot, customAgent}},
			// A row that predates 0141's execution-shape CHECK is legal while the
			// constraint is NOT VALID. The retirement cleanup deletes it without
			// re-checking the invalid legacy shape or aborting the whole upgrade.
			{`INSERT INTO schedule (
				id, team_id, bot_id, name, description, pattern, command,
				run_target, runtime_type, acp_agent_id, model_id
			) VALUES (
				$1, $2, $3, 'legacy invalid schedule', '', '0 0 * * *', 'run',
				'new_session', 'acp_agent', 'codex', $4
			)`, []any{legacySched, team.DefaultTeamID, collisionBot, modelID}},
		}
		for _, statement := range seed {
			if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
				t.Fatalf("seed external Agent migration: %v", err)
			}
		}
		if _, err := pool.Exec(ctx, `
			ALTER TABLE schedule
			ADD CONSTRAINT schedule_acp_fields_check CHECK (
				run_target <> 'new_session'
				OR (runtime_type = 'acp_agent' AND acp_agent_id IS NOT NULL AND model_id IS NULL)
				OR (COALESCE(runtime_type, 'model') = 'model' AND bot_agent_id IS NULL AND acp_agent_id IS NULL AND acp_model_id IS NULL)
			) NOT VALID`); err != nil {
			t.Fatalf("restore unvalidated schedule ACP fields constraint: %v", err)
		}

		stepUp(t, dsn, 1)
		assertDirectScheduleRequiresBotAgent(t, ctx, pool, false)

		var runtime, auth, baseURL, storedCredential string
		var enabled, deleted bool
		if err := pool.QueryRow(ctx, `
			SELECT runtime,
			       COALESCE(metadata->>'auth', ''),
			       COALESCE(metadata->>'base_url', ''),
			       agent_credential_id::text,
			       enabled,
			       deleted_at IS NOT NULL
			FROM bot_agents WHERE id = $1`, agentID,
		).Scan(&runtime, &auth, &baseURL, &storedCredential, &enabled, &deleted); err != nil {
			t.Fatalf("read retired Agent: %v", err)
		}
		if runtime != "acp" || auth != "" || baseURL != "" || storedCredential != credentialID || enabled || !deleted {
			t.Fatalf("retired Agent = runtime=%q auth=%q base_url=%q credential=%q enabled=%t deleted=%t", runtime, auth, baseURL, storedCredential, enabled, deleted)
		}

		var botRuntime string
		var defaultCleared, legacyDefaultCleared bool
		if err := pool.QueryRow(ctx, `
			SELECT
				(SELECT chat_runtime FROM bots WHERE id = $1),
				(SELECT default_bot_agent_id IS NULL AND chat_acp_agent_id IS NULL FROM bots WHERE id = $1),
				(SELECT default_bot_agent_id IS NULL AND chat_runtime = 'model' AND chat_acp_agent_id IS NULL FROM bots WHERE id = $2)`,
			botID, disabledBotID,
		).Scan(&botRuntime, &defaultCleared, &legacyDefaultCleared); err != nil {
			t.Fatalf("read retired runtime bindings: %v", err)
		}
		if botRuntime != "model" || !defaultCleared || !legacyDefaultCleared {
			t.Fatalf("retired defaults = bot_runtime=%q default_cleared=%t legacy_default_cleared=%t", botRuntime, defaultCleared, legacyDefaultCleared)
		}

		var archivedSessions, remainingSchedules, directAgents, activeCustomAgents, customSessions, customSchedules int
		if err := pool.QueryRow(ctx, `
			SELECT
				(SELECT count(*) FROM bot_sessions WHERE id IN ($1, $2, $3) AND runtime_type = 'acp_agent' AND deleted_at IS NOT NULL),
				(SELECT count(*) FROM schedule WHERE id IN ($4, $5, $6)),
				(SELECT count(*) FROM bot_agents WHERE runtime IN ('codex', 'claude-code')),
				(SELECT count(*) FROM bot_agents WHERE id = $7 AND runtime = 'acp' AND metadata->>'provider' = 'acp' AND enabled AND deleted_at IS NULL),
				(SELECT count(*) FROM bot_sessions WHERE id = $8 AND runtime_type = 'acp_agent' AND deleted_at IS NULL),
				(SELECT count(*) FROM schedule WHERE id = $9 AND runtime_type = 'acp_agent' AND acp_agent_id = 'acp')`,
			sessionID, orphanSession, disabledSess,
			scheduleID, disabledSched, legacySched,
			customAgent, customSession, customSchedule,
		).Scan(&archivedSessions, &remainingSchedules, &directAgents, &activeCustomAgents, &customSessions, &customSchedules); err != nil {
			t.Fatalf("inspect retired ACP data: %v", err)
		}
		if archivedSessions != 3 || remainingSchedules != 0 || directAgents != 0 || activeCustomAgents != 1 || customSessions != 1 || customSchedules != 1 {
			t.Fatalf("retirement = archived_sessions=%d remaining_schedules=%d direct_agents=%d active_custom_agents=%d custom_sessions=%d custom_schedules=%d", archivedSessions, remainingSchedules, directAgents, activeCustomAgents, customSessions, customSchedules)
		}

		const (
			directBotID      = "f2000000-0000-4000-8000-000000000143"
			directAgentID    = "f3000000-0000-4000-8000-000000000143"
			directSessionID  = "f4000000-0000-4000-8000-000000000143"
			directScheduleID = "f5000000-0000-4000-8000-000000000143"
			directMessageID  = "f6000000-0000-4000-8000-000000000143"
		)
		directSeed := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO bots (id, team_id, owner_user_id, name, chat_runtime, metadata)
			  VALUES ($1, $2, $3, 'direct-runtime-rollback-bot', 'model', '{}'::jsonb)`, []any{directBotID, team.DefaultTeamID, userID}},
			{`INSERT INTO bot_agents (id, team_id, bot_id, name, runtime, enabled, metadata)
			  VALUES ($1, $2, $3, 'Direct Codex', 'codex', true, '{"provider":"codex"}'::jsonb)`, []any{directAgentID, team.DefaultTeamID, directBotID}},
			{`UPDATE bots SET chat_runtime = 'codex', default_bot_agent_id = $1 WHERE id = $2`, []any{directAgentID, directBotID}},
			{`INSERT INTO bot_sessions (id, team_id, bot_id, bot_agent_id, type, session_mode, runtime_type)
			  VALUES ($1, $2, $3, $4, 'chat', 'chat', 'codex')`, []any{directSessionID, team.DefaultTeamID, directBotID, directAgentID}},
			{`INSERT INTO bot_history_messages (id, team_id, bot_id, session_id, role, content, runtime_type)
			  VALUES ($1, $2, $3, $4, 'assistant', '[]'::jsonb, 'codex')`, []any{directMessageID, team.DefaultTeamID, directBotID, directSessionID}},
			{`INSERT INTO schedule (id, team_id, bot_id, bot_agent_id, name, description, pattern, command, run_target, runtime_type)
			  VALUES ($1, $2, $3, $4, 'direct schedule', '', '0 0 * * *', 'run', 'new_session', 'codex')`, []any{directScheduleID, team.DefaultTeamID, directBotID, directAgentID}},
		}
		for _, statement := range directSeed {
			if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
				t.Fatalf("seed direct rollback data: %v", err)
			}
		}

		stepDown(t, dsn, 1)
		assertScheduleACPFieldsValidated(t, ctx, pool, false)
		var directBotReset bool
		var directAgentRows, directSessionRows, directScheduleRows, directMessageRows int
		if err := pool.QueryRow(ctx, `
			SELECT
				(SELECT chat_runtime = 'model' AND default_bot_agent_id IS NULL FROM bots WHERE id = $1),
				(SELECT count(*) FROM bot_agents WHERE id = $2),
				(SELECT count(*) FROM bot_sessions WHERE id = $3),
				(SELECT count(*) FROM schedule WHERE id = $4),
				(SELECT count(*) FROM bot_history_messages WHERE id = $5)`,
			directBotID, directAgentID, directSessionID, directScheduleID, directMessageID,
		).Scan(&directBotReset, &directAgentRows, &directSessionRows, &directScheduleRows, &directMessageRows); err != nil {
			t.Fatalf("inspect destructive direct rollback: %v", err)
		}
		if !directBotReset || directAgentRows != 0 || directSessionRows != 0 || directScheduleRows != 0 || directMessageRows != 0 {
			t.Fatalf("direct rollback = bot_reset=%t agents=%d sessions=%d schedules=%d messages=%d", directBotReset, directAgentRows, directSessionRows, directScheduleRows, directMessageRows)
		}
		migrateUpAll(t, dsn)
	})

	t.Run("canonical init contains final Bot Agent schema", func(t *testing.T) {
		ctx := context.Background()
		dsn := teamMigrationDSN(t)
		pool := resetToEmpty(t)
		applyCanonicalInitOnly(t, dsn)
		assertBotAgentsSchema(t, ctx, pool, true)
		assertBotAgentConstraintsValidated(t, ctx, pool, true)
		assertDirectScheduleRequiresBotAgent(t, ctx, pool, true)
	})
}

func assertDirectScheduleRequiresBotAgent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wantValidated bool) {
	t.Helper()
	var definition string
	var validated bool
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid), convalidated
		FROM pg_constraint
		WHERE conrelid = 'public.schedule'::regclass
		  AND conname = 'schedule_acp_fields_check'
	`).Scan(&definition, &validated); err != nil {
		t.Fatalf("inspect direct schedule constraint: %v", err)
	}
	if !strings.Contains(definition, "bot_agent_id IS NOT NULL") {
		t.Fatalf("direct schedule constraint does not require bot_agent_id: %s", definition)
	}
	if validated != wantValidated {
		t.Fatalf("direct schedule constraint validated = %t, want %t", validated, wantValidated)
	}
}

func assertScheduleACPFieldsValidated(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want bool) {
	t.Helper()
	var validated bool
	if err := pool.QueryRow(ctx, `
		SELECT convalidated
		FROM pg_constraint
		WHERE conrelid = 'public.schedule'::regclass
		  AND conname = 'schedule_acp_fields_check'
	`).Scan(&validated); err != nil {
		t.Fatalf("inspect schedule ACP fields validation: %v", err)
	}
	if validated != want {
		t.Fatalf("schedule ACP fields constraint validated = %t, want %t", validated, want)
	}
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
