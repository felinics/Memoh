//go:build integration

package db_test

import (
	"context"
	"io/fs"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/team"
)

func assertEmailTablesAbsent(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for _, table := range []string{"email_providers", "email_oauth_tokens", "bot_email_bindings", "email_outbox"} {
		var exists bool
		if err := pool.QueryRow(context.Background(), `SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if exists {
			t.Errorf("retired email table %s still exists", table)
		}
	}
}

func TestEmailRemovalMigrationDeletesMailAndPreservesAccounts(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	assertEmailTablesAbsent(t, pool)
	dsn := teamMigrationDSN(t)
	steps := countMigrationsFrom(t, "0153_remove_email.up.sql")
	stepDown(t, dsn, steps)

	const userID = "10000000-0000-4000-8000-000000000153"
	const botID = "20000000-0000-4000-8000-000000000153"
	const providerID = "30000000-0000-4000-8000-000000000153"
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users (id, username, email) VALUES ($1, 'email-removal-owner', 'owner@example.com')`, []any{userID}},
		{`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, []any{team.DefaultTeamID, userID}},
		{`INSERT INTO bots (id, team_id, owner_user_id, name) VALUES ($1, $2, $3, 'email-removal-bot')`, []any{botID, team.DefaultTeamID, userID}},
		{`INSERT INTO email_providers (id, team_id, user_id, name, provider, config) VALUES ($1, $2, $3, 'Mail', 'gmail', '{"email_address":"bot@example.com"}')`, []any{providerID, team.DefaultTeamID, userID}},
		{`INSERT INTO email_oauth_tokens (team_id, email_provider_id, access_token, refresh_token) VALUES ($1, $2, 'test-access', 'test-refresh')`, []any{team.DefaultTeamID, providerID}},
		{`INSERT INTO bot_email_bindings (team_id, bot_id, email_provider_id, email_address) VALUES ($1, $2, $3, 'bot@example.com')`, []any{team.DefaultTeamID, botID, providerID}},
		{`INSERT INTO email_outbox (team_id, provider_id, bot_id, subject, body_text) VALUES ($1, $2, $3, 'Test mail', 'Test content')`, []any{team.DefaultTeamID, providerID, botID}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed mail configuration: %v", err)
		}
	}

	stepUp(t, dsn, steps)
	assertEmailTablesAbsent(t, pool)
	var email string
	if err := pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, userID).Scan(&email); err != nil || email != "owner@example.com" {
		t.Fatalf("account email changed: email=%q err=%v", email, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bots WHERE id = $1`, botID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("bot changed: count=%d err=%v", count, err)
	}

	// Retrying the destructive migration must be harmless once mail is gone.
	up, err := fs.ReadFile(postgresMigrationsFS(t), "0153_remove_email.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(up)); err != nil {
		t.Fatalf("repeat removal: %v", err)
	}

	stepDown(t, dsn, steps)
	for _, table := range []string{"email_providers", "email_oauth_tokens", "bot_email_bindings", "email_outbox"} {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rollback must restore empty %s: count=%d err=%v", table, count, err)
		}
		var enabled, forced bool
		if err := pool.QueryRow(ctx, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass('public.' || $1)`, table).Scan(&enabled, &forced); err != nil || !enabled || !forced {
			t.Fatalf("rollback lost tenant isolation for %s: enabled=%t forced=%t err=%v", table, enabled, forced, err)
		}
	}
	assertBusinessFKsCarryTeamID(ctx, t, pool)
	stepUp(t, dsn, steps)
	assertEmailTablesAbsent(t, pool)
}

func TestEmailRemovalCanonicalSchema(t *testing.T) {
	dsn := teamMigrationDSN(t)
	applyCanonicalInitOnly(t, dsn)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	assertEmailTablesAbsent(t, pool)
}
