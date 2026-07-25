package db_test

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	dbembed "github.com/memohai/memoh/db"
	"github.com/memohai/memoh/internal/db/epoch"
)

const (
	epochRunnerUser = "epoch_runner"
	epochRunnerPass = "epoch_runner_test" //nolint:gosec // throwaway credential for the disposable integration database
	defaultTeamID   = "00000000-0000-0000-0000-000000000001"
)

func TestEpochV2FreshAndUpgradeAgainstPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	admin, err := pgxConnect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin DSN: %v", err)
	}
	defer func() { _ = admin.Close() }()

	ensureEpochRunner(t, ctx, admin)

	freshDB := "memoh_epoch_v2_fresh_test"
	upgradeDB := "memoh_epoch_v2_upgrade_test"
	mustExecSQL(t, ctx, admin, fmt.Sprintf("DROP DATABASE IF EXISTS %s", freshDB))
	mustExecSQL(t, ctx, admin, fmt.Sprintf("DROP DATABASE IF EXISTS %s", upgradeDB))
	mustExecSQL(t, ctx, admin, fmt.Sprintf("CREATE DATABASE %s OWNER %s", freshDB, epochRunnerUser))
	mustExecSQL(t, ctx, admin, fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s", freshDB, epochRunnerUser))

	adminFresh, err := pgxConnect(ctx, rewriteDB(dsn, freshDB))
	if err != nil {
		t.Fatalf("admin connect fresh: %v", err)
	}
	mustExecSQL(t, ctx, adminFresh, `CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public`)
	_ = adminFresh.Close()

	runnerDSN := rewriteUserPass(rewriteDB(dsn, freshDB), epochRunnerUser, epochRunnerPass)
	runnerDB, err := sql.Open("pgx", runnerDSN)
	if err != nil {
		t.Fatalf("open epoch_runner db: %v", err)
	}
	defer func() { _ = runnerDB.Close() }()
	runnerDB.SetMaxOpenConns(4)

	var super bool
	if err := runnerDB.QueryRowContext(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = CURRENT_USER`).Scan(&super); err != nil {
		t.Fatalf("check superuser: %v", err)
	}
	if super {
		t.Fatal("epoch_runner must not be a superuser")
	}

	postgresFS, err := fs.Sub(dbembed.MigrationsFS, "postgres")
	if err != nil {
		t.Fatalf("open embedded postgres assets: %v", err)
	}
	runner, err := epoch.New(runnerDB, postgresFS, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("create epoch runner: %v", err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatalf("epoch fresh up: %v", err)
	}
	status, err := runner.Status(ctx)
	if err != nil {
		t.Fatalf("epoch fresh status: %v", err)
	}
	if status.State != epoch.StateV2 {
		t.Fatalf("epoch fresh state = %q, want %q", status.State, epoch.StateV2)
	}
	if err := runner.Verify(ctx); err != nil {
		t.Fatalf("epoch fresh verify: %v", err)
	}

	assertOwnerCatalogSQL(t, ctx, runnerDB)
	assertZeroCrossOwnerFKSQL(t, ctx, runnerDB)
	assertObjectsOwnedByMigrate(t, ctx, runnerDB)
	assertFreshRolePermissionsSQL(t, ctx, runnerDB)
	assertSessionRouteSnapshotColumns(t, ctx, runnerDB)

	mustExecSQL(t, ctx, admin, fmt.Sprintf("CREATE DATABASE %s", upgradeDB))
	seedV1Source(t, ctx, rewriteDB(dsn, upgradeDB))
	upgradeDBConn, err := sql.Open("pgx", rewriteDB(dsn, upgradeDB))
	if err != nil {
		t.Fatalf("open upgrade db: %v", err)
	}
	defer func() { _ = upgradeDBConn.Close() }()
	upgradeDBConn.SetMaxOpenConns(4)
	upgradeRunner, err := epoch.New(upgradeDBConn, postgresFS, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("create upgrade runner: %v", err)
	}
	if err := upgradeRunner.UpgradeV2(ctx); err != nil {
		t.Fatalf("upgrade-v2: %v", err)
	}
	if err := upgradeRunner.UpgradeV2(ctx); err != nil {
		t.Fatalf("idempotent upgrade-v2: %v", err)
	}
	assertOwnerCatalogSQL(t, ctx, upgradeDBConn)
	assertZeroCrossOwnerFKSQL(t, ctx, upgradeDBConn)
	assertObjectsOwnedByMigrate(t, ctx, upgradeDBConn)
	assertFreshRolePermissionsSQL(t, ctx, upgradeDBConn)
	assertSessionRouteSnapshotColumns(t, ctx, upgradeDBConn)
	assertSessionRouteSnapshotBackfill(t, ctx, upgradeDBConn)
	upgradeStatus, err := upgradeRunner.Status(ctx)
	if err != nil {
		t.Fatalf("epoch upgrade status: %v", err)
	}
	if upgradeStatus.State != epoch.StateV2 {
		t.Fatalf("epoch upgrade state = %q, want %q", upgradeStatus.State, epoch.StateV2)
	}
	if err := upgradeRunner.Verify(ctx); err != nil {
		t.Fatalf("epoch upgrade verify: %v", err)
	}
}

// seedV1Source materializes a v1@119 database from the frozen snapshot so the
// bridge is exercised on every run rather than only where someone happened to
// provision a v1 template. The stamp is applied separately because the snapshot
// carries schema only, and the bridge refuses any source that is not recorded at
// the last approved v1 version.
func seedV1Source(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	snapshot, err := dbembed.MigrationsFS.ReadFile("postgres/legacy/v1/v1_119_schema.sql")
	if err != nil {
		t.Fatalf("read v1 snapshot: %v", err)
	}

	// pg_dump prefixes its output with session-level settings, among them
	// row_security = off and an emptied search_path. Seeding over a dedicated
	// connection that is then discarded keeps that state from leaking into the
	// pool the bridge and the assertions share.
	seed, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open v1 seed connection: %v", err)
	}
	defer func() { _ = seed.Close() }()
	seed.SetMaxOpenConns(1)

	if _, err := seed.ExecContext(ctx, string(snapshot)); err != nil {
		t.Fatalf("apply v1 snapshot: %v", err)
	}
	mustExecSQL(t, ctx, seed, `
INSERT INTO public.schema_migrations (version, dirty) VALUES (119, false)
ON CONFLICT (version) DO UPDATE SET dirty = false`)
}

func assertSessionRouteSnapshotColumns(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM information_schema.columns
		WHERE table_schema = 'agent'
		  AND table_name = 'bot_sessions'
		  AND column_name IN ('conversation_type', 'conversation_name', 'reply_target')
	`).Scan(&count); err != nil {
		t.Fatalf("query session route snapshot columns: %v", err)
	}
	if count != 3 {
		t.Fatalf("session route snapshot columns = %d, want 3", count)
	}
}

func assertSessionRouteSnapshotBackfill(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var mismatches int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM agent.bot_sessions AS session
		JOIN channel.bot_channel_routes AS route
		  ON route.team_id = session.team_id
		 AND route.id = session.route_id
		WHERE session.conversation_type IS DISTINCT FROM route.conversation_type
		   OR session.conversation_name IS DISTINCT FROM NULLIF(BTRIM(route.metadata->>'conversation_name'), '')
		   OR session.reply_target IS DISTINCT FROM route.default_reply_target
	`).Scan(&mismatches); err != nil {
		t.Fatalf("query session route snapshot backfill: %v", err)
	}
	if mismatches != 0 {
		t.Fatalf("session route snapshot backfill mismatches = %d", mismatches)
	}
}

func ensureEpochRunner(t *testing.T, ctx context.Context, admin *sql.DB) {
	t.Helper()
	mustExecSQL(t, ctx, admin, fmt.Sprintf(`
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%s') THEN
    CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOCREATEDB NOREPLICATION CREATEROLE;
  ELSE
    ALTER ROLE %s WITH LOGIN PASSWORD '%s' NOSUPERUSER NOCREATEDB NOREPLICATION CREATEROLE;
  END IF;
END $$;`, epochRunnerUser, epochRunnerUser, epochRunnerPass, epochRunnerUser, epochRunnerPass))
	mustExecSQL(t, ctx, admin, `
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_migrate') THEN
    CREATE ROLE memoh_migrate NOLOGIN NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_iam') THEN
    CREATE ROLE memoh_iam NOLOGIN NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_api') THEN
    CREATE ROLE memoh_api NOLOGIN NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_agent') THEN
    CREATE ROLE memoh_agent NOLOGIN NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_channel') THEN
    CREATE ROLE memoh_channel NOLOGIN NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_memory') THEN
    CREATE ROLE memoh_memory NOLOGIN NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_runtime') THEN
    CREATE ROLE memoh_runtime NOLOGIN NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_model') THEN
    CREATE ROLE memoh_model NOLOGIN NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'memoh_media') THEN
    CREATE ROLE memoh_media NOLOGIN NOINHERIT;
  END IF;
END $$;
GRANT memoh_migrate, memoh_iam, memoh_api, memoh_agent, memoh_channel, memoh_memory, memoh_runtime, memoh_model, memoh_media TO epoch_runner;
ALTER ROLE memoh_iam SET search_path TO iam, public;
ALTER ROLE memoh_api SET search_path TO api, iam, public;
ALTER ROLE memoh_agent SET search_path TO agent, iam, public;
ALTER ROLE memoh_channel SET search_path TO channel, iam, public;
ALTER ROLE memoh_memory SET search_path TO memory, iam, public;
ALTER ROLE memoh_runtime SET search_path TO runtime, iam, public;
ALTER ROLE memoh_model SET search_path TO model, iam, public;
ALTER ROLE memoh_media SET search_path TO media, iam, public;
ALTER ROLE memoh_migrate SET search_path TO iam, public;
`)
}

func assertObjectsOwnedByMigrate(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT n.nspname, c.relname, pg_get_userbyid(c.relowner)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = ANY($1)
		  AND c.relkind IN ('r','S','v')
		ORDER BY 1, 2`, ownerOrder)
	if err != nil {
		t.Fatalf("list object owners: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var schema, rel, owner string
		if err := rows.Scan(&schema, &rel, &owner); err != nil {
			t.Fatalf("scan owner: %v", err)
		}
		if owner != "memoh_migrate" {
			t.Fatalf("%s.%s owned by %q, want memoh_migrate", schema, rel, owner)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, schema := range ownerOrder {
		var owner string
		if err := db.QueryRowContext(ctx, `
			SELECT pg_get_userbyid(c.relowner)
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = 'goose_db_version' AND c.relkind = 'r'
		`, schema).Scan(&owner); err != nil {
			t.Fatalf("version table %s.goose_db_version: %v", schema, err)
		}
		if owner != "memoh_migrate" {
			t.Fatalf("%s.goose_db_version owned by %q, want memoh_migrate", schema, owner)
		}
	}
}

func assertOwnerCatalogSQL(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT n.nspname, c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r'
		  AND n.nspname = ANY($1)
		  AND c.relname <> 'goose_db_version'
		ORDER BY 1, 2`, ownerOrder)
	if err != nil {
		t.Fatalf("list owner tables: %v", err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]string{}
	for rows.Next() {
		var schema, table string
		if err := rows.Scan(&schema, &table); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[table] = schema
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 58 {
		t.Fatalf("owned tables in catalog = %d, want 58", len(got))
	}
	for owner, tables := range ownerTables {
		for _, table := range tables {
			if got[table] != owner {
				t.Fatalf("catalog %s in schema %q, want %q", table, got[table], owner)
			}
		}
	}
}

func assertZeroCrossOwnerFKSQL(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var n int
	err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_constraint c
		JOIN pg_class cs ON cs.oid = c.conrelid
		JOIN pg_namespace ns ON ns.oid = cs.relnamespace
		JOIN pg_class cd ON cd.oid = c.confrelid
		JOIN pg_namespace nd ON nd.oid = cd.relnamespace
		WHERE c.contype = 'f'
		  AND ns.nspname = ANY($1)
		  AND nd.nspname = ANY($1)
		  AND ns.nspname IS DISTINCT FROM nd.nspname`, ownerOrder).Scan(&n)
	if err != nil {
		t.Fatalf("count cross-owner fk: %v", err)
	}
	if n != 0 {
		t.Fatalf("cross-owner FK count = %d, want 0", n)
	}
}

func assertFreshRolePermissionsSQL(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	samples := map[string]string{
		"iam":     "iam.teams",
		"api":     "api.bots",
		"model":   "model.providers",
		"media":   "media.storage_providers",
		"agent":   "agent.bot_sessions",
		"channel": "channel.channel_identities",
		"memory":  "memory.memory_providers",
		"runtime": "runtime.containers",
	}
	roles := map[string]string{
		"iam":     "memoh_iam",
		"api":     "memoh_api",
		"model":   "memoh_model",
		"media":   "memoh_media",
		"agent":   "memoh_agent",
		"channel": "memoh_channel",
		"memory":  "memoh_memory",
		"runtime": "memoh_runtime",
	}
	for owner, role := range roles {
		table := samples[owner]
		mustExecSQL(t, ctx, db, fmt.Sprintf(`
			SET ROLE %s;
			SELECT set_config('memoh.team_id', '%s', true);
			SELECT count(*) FROM %s;
			RESET ROLE;
		`, role, defaultTeamID, table))
		mustExecSQL(t, ctx, db, fmt.Sprintf(`SET ROLE %s; SELECT count(*) FROM %s.goose_db_version; RESET ROLE;`, role, owner))
		_, err := db.ExecContext(ctx, fmt.Sprintf(`
			SET ROLE %s;
			INSERT INTO %s.goose_db_version (version_id, is_applied) VALUES (999, true);
		`, role, owner))
		if err == nil {
			t.Fatalf("%s should not INSERT into %s.goose_db_version", role, owner)
		}
		mustExecSQL(t, ctx, db, `RESET ROLE`)

		other := "api.bots"
		if owner == "api" {
			other = "agent.bot_sessions"
		}
		_, err = db.ExecContext(ctx, fmt.Sprintf(`
			SET ROLE %s;
			SELECT set_config('memoh.team_id', '%s', true);
			SELECT count(*) FROM %s;
		`, role, defaultTeamID, other))
		if err == nil {
			t.Fatalf("%s should not read other-owner table %s", role, other)
		}
		mustExecSQL(t, ctx, db, `RESET ROLE`)

		_, err = db.ExecContext(ctx, fmt.Sprintf(`
			SET ROLE %s;
			SELECT set_config('memoh.team_id', '%s', true);
			INSERT INTO %s SELECT * FROM %s WHERE false;
		`, role, defaultTeamID, other, other))
		if err == nil {
			t.Fatalf("%s should not write other-owner table %s", role, other)
		}
		mustExecSQL(t, ctx, db, `RESET ROLE`)

		var relkind string
		var rls bool
		if err := db.QueryRowContext(ctx, `
			SELECT c.relkind, c.relrowsecurity
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname || '.' || c.relname = $1
		`, table).Scan(&relkind, &rls); err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if relkind == "r" && !rls {
			t.Fatalf("expected RLS enabled on %s", table)
		}
	}
}

func pgxConnect(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func mustExecSQL(t *testing.T, ctx context.Context, db *sql.DB, sqlText string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, sqlText); err != nil {
		t.Fatalf("exec %s: %v", sqlText, err)
	}
}

func rewriteDB(dsn, dbName string) string {
	if i := strings.LastIndex(dsn, "/"); i >= 0 {
		prefix := dsn[:i+1]
		rest := dsn[i+1:]
		if j := strings.Index(rest, "?"); j >= 0 {
			return prefix + dbName + rest[j:]
		}
		return prefix + dbName
	}
	return dsn
}

func rewriteUserPass(dsn, user, pass string) string {
	withoutScheme, ok := strings.CutPrefix(dsn, "postgres://")
	if !ok {
		withoutScheme, ok = strings.CutPrefix(dsn, "postgresql://")
		if !ok {
			return dsn
		}
		return "postgresql://" + user + ":" + pass + "@" + withoutScheme[strings.Index(withoutScheme, "@")+1:]
	}
	at := strings.Index(withoutScheme, "@")
	if at < 0 {
		return dsn
	}
	return "postgres://" + user + ":" + pass + "@" + withoutScheme[at+1:]
}
