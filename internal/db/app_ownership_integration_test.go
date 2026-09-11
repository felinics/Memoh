//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

const (
	appTeamID   = "00000000-0000-0000-0000-000000000001"
	appUserID   = "10000000-0000-4000-8000-000000000001"
	appBotOneID = "20000000-0000-4000-8000-000000000001"
	appRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestRemoteRuntimeDeletionRemovesAppInstallation(t *testing.T) {
	ctx := context.Background()
	pool := teamScopedPool(t)
	seedAppBot(t, pool)
	targetID := seedRemoteRuntimeTarget(t, pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO bot_app_installations
			(id, bot_id, workspace_target_id, registry_id, app_id, revision, status)
		VALUES ($1, $2, $3, 'openai', 'documents', $4, 'installed')`,
		"30000000-0000-4000-8000-000000000001", appBotOneID, targetID, appRevision); err != nil {
		t.Fatalf("seed App: %v", err)
	}

	store, err := postgresstore.New(pool)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.DeleteMount(ctx, appBotOneID, targetID); err != nil {
		t.Fatalf("DeleteMount() with installed App: %v", err)
	}
	var appCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM bot_app_installations
		WHERE bot_id = $1 AND workspace_target_id = $2`, appBotOneID, targetID).Scan(&appCount); err != nil {
		t.Fatalf("count App installations: %v", err)
	}
	if appCount != 0 {
		t.Fatalf("App installation count = %d, want 0", appCount)
	}
	var bindingCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM bot_remote_runtime_bindings
		WHERE bot_id = $1 AND id = $2`, appBotOneID, targetID).Scan(&bindingCount); err != nil {
		t.Fatalf("count target bindings: %v", err)
	}
	if bindingCount != 0 {
		t.Fatalf("target binding count = %d, want 0", bindingCount)
	}
}

func TestUserRuntimeRevocationRemovesAppInstallations(t *testing.T) {
	ctx := context.Background()
	pool := teamScopedPool(t)
	seedAppBot(t, pool)
	const runtimeID = "50000000-0000-4000-8000-000000000002"
	const targetID = "60000000-0000-4000-8000-000000000002"
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_runtimes (id, user_id, name, api_token) VALUES ($1, $2, 'app-runtime-2', 'token-2')`,
		runtimeID, appUserID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO bot_remote_runtime_bindings (id, bot_id, runtime_id) VALUES ($1, $2, $3)`,
		targetID, appBotOneID, runtimeID); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bot_app_installations
			(id, bot_id, workspace_target_id, registry_id, app_id, revision, status)
		VALUES ($1, $2, $3, 'openai', 'documents', $4, 'installed')`,
		"30000000-0000-4000-8000-000000000002", appBotOneID, targetID, appRevision); err != nil {
		t.Fatalf("seed runtime App: %v", err)
	}

	store, err := postgresstore.New(pool)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.RevokeUserRuntime(ctx, runtimeID, appUserID); err != nil {
		t.Fatalf("RevokeUserRuntime(): %v", err)
	}
	var appCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM bot_app_installations
		WHERE bot_id = $1 AND workspace_target_id = $2`, appBotOneID, targetID).Scan(&appCount); err != nil {
		t.Fatalf("count revoked runtime App installations: %v", err)
	}
	if appCount != 0 {
		t.Fatalf("revoked runtime App installation count = %d, want 0", appCount)
	}
}

func seedRemoteRuntimeTarget(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	const runtimeID = "50000000-0000-4000-8000-000000000001"
	const targetID = "60000000-0000-4000-8000-000000000001"
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO user_runtimes (id, user_id, name, api_token) VALUES ($1, $2, 'app-runtime', 'token')`, runtimeID, appUserID); err != nil {
		t.Fatalf("seed Remote Runtime: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bot_remote_runtime_bindings (id, bot_id, runtime_id) VALUES ($1, $2, $3)`, targetID, appBotOneID, runtimeID); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	return targetID
}

func teamScopedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	_ = freshMigratedDB(t)
	cfg, err := pgxpool.ParseConfig(teamMigrationDSN(t))
	if err != nil {
		t.Fatalf("parse test DSN: %v", err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SELECT set_config('memoh.team_id', $1, false)`, appTeamID)
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("create team pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedAppBot(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, username) VALUES ($1, 'app-owner')`, []any{appUserID}},
		{`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, []any{appTeamID, appUserID}},
		{`INSERT INTO bots (id, team_id, owner_user_id, name) VALUES ($3, $2, $1, 'bot-one')`, []any{appUserID, appTeamID, appBotOneID}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed App fixture: %v", err)
		}
	}
}

// The unpublished App migration replaces the pre-App installation schema.
// Exercise the exact boundary rather than depending on the latest migration.
func TestAppMigrationRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := teamScopedPool(t)
	dsn := teamMigrationDSN(t)
	migrateTo(t, dsn, 148)
	var oldTable, appTable bool
	inspect := func() {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT
			to_regclass('public.bot_skill_package_installations') IS NOT NULL,
			to_regclass('public.bot_app_installations') IS NOT NULL`).Scan(&oldTable, &appTable); err != nil {
			t.Fatal(err)
		}
	}
	inspect()
	if !oldTable || appTable {
		t.Fatalf("version 148 relations: old=%v app=%v", oldTable, appTable)
	}
	migrateTo(t, dsn, 149)
	inspect()
	if oldTable || !appTable {
		t.Fatalf("version 149 relations: old=%v app=%v", oldTable, appTable)
	}
	seedAppBot(t, pool)
	var installationID string
	if err := pool.QueryRow(ctx, `INSERT INTO bot_app_installations
		(bot_id, workspace_target_id, registry_id, app_id, revision, status)
		VALUES ($1, 'native', 'memoh', 'pdf', $2, 'installed') RETURNING id`, appBotOneID, appRevision).Scan(&installationID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bot_app_dependency_refs (installation_id, dependency_id)
		VALUES ($1, 'python')`, installationID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM bot_app_installations WHERE id=$1`, installationID); err != nil {
		t.Fatal(err)
	}
	var refs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_app_dependency_refs WHERE installation_id=$1`, installationID).Scan(&refs); err != nil || refs != 0 {
		t.Fatalf("references after removal = %d, err=%v", refs, err)
	}
	migrateTo(t, dsn, 148)
	inspect()
	if !oldTable || appTable {
		t.Fatalf("rollback relations: old=%v app=%v", oldTable, appTable)
	}
	migrateTo(t, dsn, 149)
}
