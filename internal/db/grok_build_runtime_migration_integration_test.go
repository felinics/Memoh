//go:build integration

package db_test

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/team"
)

func TestGrokBuildRuntimeMigration(t *testing.T) {
	ctx := context.Background()
	dsn := teamMigrationDSN(t)
	pool := freshMigratedDB(t)

	const (
		userID     = "10000000-0000-4000-8000-000000000167"
		botID      = "20000000-0000-4000-8000-000000000167"
		agentID    = "30000000-0000-4000-8000-000000000167"
		sessionID  = "40000000-0000-4000-8000-000000000167"
		scheduleID = "50000000-0000-4000-8000-000000000167"
		messageID  = "60000000-0000-4000-8000-000000000167"
		kind       = "grok-build"
		// The constraints still enumerate the vocabulary: a kind no migration
		// admitted is refused.
		unknown = "kind-added-without-a-migration"
	)
	rejects := func(name, query string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, query, args...)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("%s: error = %v, want check_violation", name, err)
		}
	}
	seed := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, username) VALUES ($1, 'grok-build-migration-owner')`, []any{userID}},
		{`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, []any{team.DefaultTeamID, userID}},
		{`INSERT INTO bots (id, team_id, owner_user_id, name, metadata)
		  VALUES ($1, $2, $3, 'grok-build-migration-bot', '{}'::jsonb)`, []any{botID, team.DefaultTeamID, userID}},
		{`INSERT INTO bot_agents (id, team_id, bot_id, name, runtime, enabled, metadata)
		  VALUES ($1, $2, $3, 'Grok Build', $4, true, '{}'::jsonb)`, []any{agentID, team.DefaultTeamID, botID, kind}},
		{`UPDATE bots SET chat_runtime = $1, default_bot_agent_id = $2 WHERE id = $3`, []any{kind, agentID, botID}},
		{`INSERT INTO bot_sessions (id, team_id, bot_id, bot_agent_id, type, session_mode, runtime_type)
		  VALUES ($1, $2, $3, $4, 'chat', 'chat', $5)`, []any{sessionID, team.DefaultTeamID, botID, agentID, kind}},
		{`INSERT INTO bot_history_messages (id, team_id, bot_id, session_id, role, content, runtime_type)
		  VALUES ($1, $2, $3, $4, 'assistant', '[]'::jsonb, $5)`, []any{messageID, team.DefaultTeamID, botID, sessionID, kind}},
		{`INSERT INTO schedule (id, team_id, bot_id, bot_agent_id, name, description, pattern, command, run_target, runtime_type)
		  VALUES ($1, $2, $3, $4, 'direct schedule', '', '0 0 * * *', 'run', 'new_session', $5)`, []any{scheduleID, team.DefaultTeamID, botID, agentID, kind}},
		{`INSERT INTO agent_authorizations (team_id, owner_user_id, runtime, auth_kind, status, encrypted_payload, encryption_nonce, expires_at)
		  VALUES ($1, $2, $3, 'xai_api_key', 'ready', '\x00', '\x00', now() + interval '30 minutes')`, []any{team.DefaultTeamID, userID, kind}},
	}
	for _, statement := range seed {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed grok-build rows: %v", err)
		}
	}

	rejects("direct schedule without bot agent", `
		INSERT INTO schedule (team_id, bot_id, name, description, pattern, command, run_target, runtime_type)
		VALUES ($1, $2, 'agentless', '', '0 0 * * *', 'run', 'new_session', $3)`, team.DefaultTeamID, botID, kind)
	rejects("unknown session runtime", `UPDATE bot_sessions SET runtime_type = $1 WHERE id = $2`, unknown, sessionID)
	rejects("unknown bot runtime", `UPDATE bots SET chat_runtime = $1 WHERE id = $2`, unknown, botID)
	rejects("unknown authorization runtime", `UPDATE agent_authorizations SET runtime = $1 WHERE owner_user_id = $2`, unknown, userID)

	migrateTo(t, dsn, 166)
	var botReset bool
	var agents, sessions, schedules, messages, authorizations int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT chat_runtime = 'model' AND default_bot_agent_id IS NULL FROM bots WHERE id = $1),
			(SELECT count(*) FROM bot_agents WHERE id = $2),
			(SELECT count(*) FROM bot_sessions WHERE id = $3),
			(SELECT count(*) FROM schedule WHERE id = $4),
			(SELECT count(*) FROM bot_history_messages WHERE id = $5),
			(SELECT count(*) FROM agent_authorizations WHERE owner_user_id = $6)`,
		botID, agentID, sessionID, scheduleID, messageID, userID,
	).Scan(&botReset, &agents, &sessions, &schedules, &messages, &authorizations); err != nil {
		t.Fatalf("inspect rollback: %v", err)
	}
	if !botReset || agents != 1 || sessions != 0 || schedules != 0 || messages != 0 || authorizations != 0 {
		t.Fatalf("rollback = bot_reset=%t agents=%d sessions=%d schedules=%d messages=%d authorizations=%d", botReset, agents, sessions, schedules, messages, authorizations)
	}
	rejects("enumeration restored", `UPDATE bots SET chat_runtime = $1 WHERE id = $2`, kind, botID)

	migrateUpAll(t, dsn)
	if _, err := pool.Exec(ctx, `UPDATE bots SET chat_runtime = $1 WHERE id = $2`, kind, botID); err != nil {
		t.Fatalf("re-applied migration rejects grok-build: %v", err)
	}
	migrated := runtimeKindConstraints(t, ctx, pool)

	t.Run("canonical init matches", func(t *testing.T) {
		canonicalPool := resetToEmpty(t)
		applyCanonicalInitOnly(t, teamMigrationDSN(t))
		if canonical := runtimeKindConstraints(t, ctx, canonicalPool); !maps.Equal(canonical, migrated) {
			t.Fatalf("canonical constraints = %v, migrated = %v", canonical, migrated)
		}
	})
}

func runtimeKindConstraints(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT conname, pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conname IN (
			'bots_chat_runtime_check',
			'bot_sessions_runtime_type_check',
			'bot_history_messages_runtime_type_check',
			'schedule_runtime_type_check',
			'schedule_acp_fields_check',
			'agent_authorizations_runtime_check'
		)`)
	if err != nil {
		t.Fatalf("inspect runtime kind constraints: %v", err)
	}
	defer rows.Close()
	definitions := map[string]string{}
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatalf("scan runtime kind constraint: %v", err)
		}
		definitions[name] = definition
	}
	if err := rows.Err(); err != nil || len(definitions) != 6 {
		t.Fatalf("runtime kind constraints = %v, err = %v, want 6", definitions, err)
	}
	return definitions
}
