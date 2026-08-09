//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/internal/team"
)

// seedPeerGrantBots creates a team member and two bots to hang peer grants off.
func seedPeerGrantBots(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (calleeID, callerID string) {
	t.Helper()
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username) VALUES ('peer-owner') RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, team.DefaultTeamID, userID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO bots (team_id, owner_user_id, name) VALUES ($1, $2, 'peer-callee')
		RETURNING id`, team.DefaultTeamID, userID).Scan(&calleeID); err != nil {
		t.Fatalf("seed callee bot: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO bots (team_id, owner_user_id, name) VALUES ($1, $2, 'peer-caller')
		RETURNING id`, team.DefaultTeamID, userID).Scan(&callerID); err != nil {
		t.Fatalf("seed caller bot: %v", err)
	}
	return calleeID, callerID
}

func insertPeerGrant(ctx context.Context, pool *pgxpool.Pool, calleeID, subjectType string, subjectBotID any, permissions string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO bot_peer_grants (team_id, bot_id, subject_type, subject_bot_id, permissions)
		VALUES ($1, $2, $3, $4, $5::jsonb)`,
		team.DefaultTeamID, calleeID, subjectType, subjectBotID, permissions)
	return err
}

func TestBotPeerGrantsMigrationRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := teamMigrationDSN(t)

	steps := countMigrationsFrom(t, "0132_bot_peer_grants.up.sql")

	assertPeerGrantTable(t, ctx, pool, true)
	stepDown(t, dsn, steps)
	assertPeerGrantTable(t, ctx, pool, false)
	stepUp(t, dsn, steps)
	assertPeerGrantTable(t, ctx, pool, true)
}

// TestBotPeerGrantsVocabularyIsEnforcedInDatabase pins the reason peer grants
// live in their own table: the user scopes must be unrepresentable here, not
// merely rejected by the Go validator that happens to run in front of it.
func TestBotPeerGrantsVocabularyIsEnforcedInDatabase(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	calleeID, callerID := seedPeerGrantBots(t, ctx, pool)

	for _, permissions := range []string{
		`["manage"]`,
		`["chat"]`,
		`["workspace_read"]`,
		`["workspace_write"]`,
		`["workspace_exec"]`,
		`["contact","manage"]`,
		`[]`,
		`"contact"`,
	} {
		err := insertPeerGrant(ctx, pool, calleeID, "bot", callerID, permissions)
		if sqlState(err) != "23514" {
			t.Fatalf("permissions %s SQLSTATE = %q, want 23514", permissions, sqlState(err))
		}
	}

	if err := insertPeerGrant(ctx, pool, calleeID, "bot", callerID, `["discover","contact","delegate"]`); err != nil {
		t.Fatalf("peer vocabulary insert: %v", err)
	}
}

func TestBotPeerGrantsRejectsSelfEdgeAndSubjectShape(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	calleeID, callerID := seedPeerGrantBots(t, ctx, pool)

	// A bot cannot be granted access to itself.
	if err := insertPeerGrant(ctx, pool, calleeID, "bot", calleeID, `["contact"]`); sqlState(err) != "23514" {
		t.Fatalf("self edge SQLSTATE = %q, want 23514", sqlState(err))
	}
	// A directed grant must name its subject.
	if err := insertPeerGrant(ctx, pool, calleeID, "bot", nil, `["contact"]`); sqlState(err) != "23514" {
		t.Fatalf("subjectless bot grant SQLSTATE = %q, want 23514", sqlState(err))
	}
	// A blanket grant must not name one.
	if err := insertPeerGrant(ctx, pool, calleeID, "any_bot", callerID, `["contact"]`); sqlState(err) != "23514" {
		t.Fatalf("any_bot with subject SQLSTATE = %q, want 23514", sqlState(err))
	}
	// Unknown subject types are rejected too.
	if err := insertPeerGrant(ctx, pool, calleeID, "user", nil, `["contact"]`); sqlState(err) != "23514" {
		t.Fatalf("user subject SQLSTATE = %q, want 23514", sqlState(err))
	}
}

func TestBotPeerGrantsUniqueEdgeCoversBlanketRow(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	calleeID, callerID := seedPeerGrantBots(t, ctx, pool)

	if err := insertPeerGrant(ctx, pool, calleeID, "bot", callerID, `["contact"]`); err != nil {
		t.Fatalf("first directed grant: %v", err)
	}
	if err := insertPeerGrant(ctx, pool, calleeID, "bot", callerID, `["discover"]`); sqlState(err) != "23505" {
		t.Fatalf("duplicate directed grant SQLSTATE = %q, want 23505", sqlState(err))
	}

	if err := insertPeerGrant(ctx, pool, calleeID, "any_bot", nil, `["contact"]`); err != nil {
		t.Fatalf("first blanket grant: %v", err)
	}
	// NULLS NOT DISTINCT is what makes this a conflict; a plain UNIQUE would let
	// the blanket row be inserted without limit.
	if err := insertPeerGrant(ctx, pool, calleeID, "any_bot", nil, `["discover"]`); sqlState(err) != "23505" {
		t.Fatalf("duplicate blanket grant SQLSTATE = %q, want 23505", sqlState(err))
	}
}

func TestBotPeerGrantsCascadeFromBothEnds(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	calleeID, callerID := seedPeerGrantBots(t, ctx, pool)

	if err := insertPeerGrant(ctx, pool, calleeID, "bot", callerID, `["contact"]`); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	// Deleting the caller must clear the edge; a dangling grant would keep
	// authorizing a bot that no longer exists once an id is reused.
	if _, err := pool.Exec(ctx, `DELETE FROM bots WHERE team_id=$1 AND id=$2`, team.DefaultTeamID, callerID); err != nil {
		t.Fatalf("delete caller bot: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_peer_grants WHERE bot_id=$1`, calleeID).Scan(&remaining); err != nil {
		t.Fatalf("count grants after caller delete: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("grants after caller delete = %d, want 0", remaining)
	}
}

func TestBotPeerGrantsForcesRowLevelSecurity(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)

	var enabled, forced bool
	if err := pool.QueryRow(ctx, `
		SELECT relrowsecurity, relforcerowsecurity
		FROM pg_class WHERE oid = 'public.bot_peer_grants'::regclass`).Scan(&enabled, &forced); err != nil {
		t.Fatalf("inspect RLS flags: %v", err)
	}
	if !enabled || !forced {
		t.Fatalf("RLS enabled=%v forced=%v, want both true", enabled, forced)
	}

	var policies int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_policies
		WHERE schemaname='public' AND tablename='bot_peer_grants'`).Scan(&policies); err != nil {
		t.Fatalf("count policies: %v", err)
	}
	if policies != 4 {
		t.Fatalf("policy count = %d, want 4", policies)
	}
}

func assertPeerGrantTable(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want bool) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema='public' AND table_name='bot_peer_grants')`).Scan(&exists); err != nil {
		t.Fatalf("inspect bot_peer_grants: %v", err)
	}
	if exists != want {
		t.Fatalf("bot_peer_grants exists = %v, want %v", exists, want)
	}
}
