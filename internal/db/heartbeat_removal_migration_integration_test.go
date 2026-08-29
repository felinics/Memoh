//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/team"
)

func TestHeartbeatRemovalMigrationDeletesDescendantSessions(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := teamMigrationDSN(t)
	steps := countMigrationsFrom(t, "0140_drop_heartbeat.up.sql")

	// Restore the legacy Heartbeat schema, seed a descendant tree, then run
	// the real removal migration and every migration that follows it.
	stepDown(t, dsn, steps)

	const (
		userID             = "10000000-0000-4000-8000-000000000140"
		botID              = "20000000-0000-4000-8000-000000000140"
		heartbeatSessionID = "30000000-0000-4000-8000-000000000140"
		childSessionID     = "40000000-0000-4000-8000-000000000140"
		grandchildID       = "50000000-0000-4000-8000-000000000140"
		retainedSessionID  = "60000000-0000-4000-8000-000000000140"
	)

	if _, err := pool.Exec(ctx, `
		INSERT INTO public.users (id, username)
		VALUES ($1, 'heartbeat-removal-owner')`, userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.team_members (team_id, user_id)
		VALUES ($1, $2)`, team.DefaultTeamID, userID); err != nil {
		t.Fatalf("seed team membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.bots (id, team_id, owner_user_id, name)
		VALUES ($1, $2, $3, 'heartbeat-removal-bot')`, botID, team.DefaultTeamID, userID); err != nil {
		t.Fatalf("seed bot: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.bot_sessions (
			id, team_id, bot_id, type, session_mode, parent_session_id
		)
		VALUES
			($1, $5, $6, 'heartbeat', 'heartbeat', NULL),
			($2, $5, $6, 'subagent', 'subagent', $1),
			($3, $5, $6, 'subagent', 'subagent', $2),
			($4, $5, $6, 'chat', 'chat', NULL)`,
		heartbeatSessionID, childSessionID, grandchildID, retainedSessionID,
		team.DefaultTeamID, botID,
	); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.bot_history_messages (
			id, team_id, bot_id, session_id, role, content, session_mode
		)
		VALUES
			('70000000-0000-4000-8000-000000000140', $1, $2, $3, 'user', '"heartbeat"'::jsonb, 'heartbeat'),
			('80000000-0000-4000-8000-000000000140', $1, $2, $4, 'assistant', '"child"'::jsonb, 'subagent'),
			('90000000-0000-4000-8000-000000000140', $1, $2, $5, 'assistant', '"grandchild"'::jsonb, 'subagent'),
			('a0000000-0000-4000-8000-000000000140', $1, $2, $6, 'user', '"retained"'::jsonb, 'chat')`,
		team.DefaultTeamID, botID, heartbeatSessionID, childSessionID, grandchildID, retainedSessionID,
	); err != nil {
		t.Fatalf("seed messages: %v", err)
	}

	stepUp(t, dsn, steps)

	var retiredSessions, retainedSessions int
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE id IN ($1, $2, $3)),
			count(*) FILTER (WHERE id = $4)
		FROM public.bot_sessions`,
		heartbeatSessionID, childSessionID, grandchildID, retainedSessionID,
	).Scan(&retiredSessions, &retainedSessions); err != nil {
		t.Fatalf("inspect migrated sessions: %v", err)
	}
	if retiredSessions != 0 || retainedSessions != 1 {
		t.Fatalf(
			"session counts after migration = retired:%d retained:%d, want retired:0 retained:1",
			retiredSessions, retainedSessions,
		)
	}

	var retiredMessages, retainedMessages int
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE id IN (
				'70000000-0000-4000-8000-000000000140',
				'80000000-0000-4000-8000-000000000140',
				'90000000-0000-4000-8000-000000000140'
			)),
			count(*) FILTER (WHERE id = 'a0000000-0000-4000-8000-000000000140')
		FROM public.bot_history_messages`,
	).Scan(&retiredMessages, &retainedMessages); err != nil {
		t.Fatalf("inspect migrated messages: %v", err)
	}
	if retiredMessages != 0 || retainedMessages != 1 {
		t.Fatalf(
			"message counts after migration = retired:%d retained:%d, want retired:0 retained:1",
			retiredMessages, retainedMessages,
		)
	}
}
