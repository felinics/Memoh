package db

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCompactionTargetPercentMigrationFiles(t *testing.T) {
	baseline := readEmbeddedMigration(t, "postgres/migrations/0001_init.up.sql")
	if !strings.Contains(baseline, "compaction_target_percent INTEGER") || strings.Contains(baseline, "compaction_ratio INTEGER") {
		t.Fatal("canonical schema must contain only the nullable compaction target percent")
	}
	up := readEmbeddedMigration(t, "postgres/migrations/0125_compaction_target_percent.up.sql")
	if !strings.Contains(up, "100 - compaction_ratio") || !strings.Contains(up, "compaction_threshold > 0") {
		t.Fatal("up migration must map legacy manual ratios to target percentages")
	}
	if !strings.Contains(up, "NO FORCE ROW LEVEL SECURITY") || !strings.Contains(up, "DISABLE ROW LEVEL SECURITY") {
		t.Fatal("up migration must suspend FORCE RLS for its all-team backfill")
	}
	down := readEmbeddedMigration(t, "postgres/migrations/0125_compaction_target_percent.down.sql")
	if !strings.Contains(down, "GREATEST(1, LEAST(100, 100 - compaction_target_percent))") {
		t.Fatal("down migration must restore a clamped legacy ratio")
	}
	if !strings.Contains(down, "NO FORCE ROW LEVEL SECURITY") || !strings.Contains(down, "DISABLE ROW LEVEL SECURITY") {
		t.Fatal("down migration must suspend FORCE RLS for its all-team backfill")
	}
}

func TestCompactionTargetPercentMigrationPostgresPath(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	schema := "compaction_target_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+quotedSchema); err != nil {
		t.Fatalf("set search path: %v", err)
	}
	if _, err := tx.Exec(ctx, `
CREATE FUNCTION migration_current_team_id()
RETURNS INTEGER
LANGUAGE sql
STABLE
AS $$ SELECT current_setting('memoh.team_id')::integer $$;

CREATE TABLE bots (
  id INTEGER PRIMARY KEY,
  team_id INTEGER NOT NULL,
  compaction_threshold INTEGER NOT NULL DEFAULT 0,
  compaction_ratio INTEGER NOT NULL DEFAULT 80
);
INSERT INTO bots (id, team_id, compaction_threshold, compaction_ratio)
VALUES (1, 1, 0, 80), (2, 1, 100000, 80), (3, 2, 100000, 1);

ALTER TABLE bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE bots FORCE ROW LEVEL SECURITY;
CREATE POLICY bots_team_isolation ON bots
  USING (team_id = migration_current_team_id())
  WITH CHECK (team_id = migration_current_team_id());
`); err != nil {
		t.Fatalf("create legacy fixture: %v", err)
	}
	migrationRole := migrationTestRole(t, ctx, tx, schema)

	up := readEmbeddedMigration(t, "postgres/migrations/0125_compaction_target_percent.up.sql")
	down := readEmbeddedMigration(t, "postgres/migrations/0125_compaction_target_percent.down.sql")
	execMigrationAsRole(t, ctx, tx, migrationRole, up, "apply 0125 up")
	execMigrationAsRole(t, ctx, tx, migrationRole, up, "reapply 0125 up")
	assertRLSForced(t, ctx, tx)
	assertColumnExists(t, ctx, tx, schema, "bots", "compaction_ratio", false)
	assertColumnExists(t, ctx, tx, schema, "bots", "compaction_target_percent", true)
	assertNullableInt(t, ctx, tx, 1, nil)
	assertNullableInt(t, ctx, tx, 2, intPointer(20))
	assertNullableInt(t, ctx, tx, 3, intPointer(99))

	execMigrationAsRole(t, ctx, tx, migrationRole, down, "apply 0125 down")
	execMigrationAsRole(t, ctx, tx, migrationRole, down, "reapply 0125 down")
	assertRLSForced(t, ctx, tx)
	assertColumnExists(t, ctx, tx, schema, "bots", "compaction_target_percent", false)
	assertColumnExists(t, ctx, tx, schema, "bots", "compaction_ratio", true)
	assertInt(t, ctx, tx, 1, 80)
	assertInt(t, ctx, tx, 2, 80)
	assertInt(t, ctx, tx, 3, 1)

	execMigrationAsRole(t, ctx, tx, migrationRole, up, "apply 0125 up after down")
	assertRLSForced(t, ctx, tx)
	assertNullableInt(t, ctx, tx, 1, nil)
	assertNullableInt(t, ctx, tx, 2, intPointer(20))
	assertNullableInt(t, ctx, tx, 3, intPointer(99))
}

func migrationTestRole(t *testing.T, ctx context.Context, tx pgx.Tx, schema string) string {
	t.Helper()
	var isSuperuser bool
	if err := tx.QueryRow(ctx, `
SELECT rolsuper
FROM pg_roles
WHERE rolname = current_user
`).Scan(&isSuperuser); err != nil {
		t.Fatalf("read migration user privileges: %v", err)
	}
	if !isSuperuser {
		return ""
	}
	role := "compaction_migrator_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedRole := pgx.Identifier{role}.Sanitize()
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, "CREATE ROLE "+quotedRole+" NOLOGIN"); err != nil {
		t.Fatalf("create non-superuser migration role: %v", err)
	}
	if _, err := tx.Exec(ctx, "GRANT USAGE ON SCHEMA "+quotedSchema+" TO "+quotedRole); err != nil {
		t.Fatalf("grant migration schema usage: %v", err)
	}
	if _, err := tx.Exec(ctx, "ALTER TABLE bots OWNER TO "+quotedRole); err != nil {
		t.Fatalf("transfer bots ownership: %v", err)
	}
	return role
}

func execMigrationAsRole(t *testing.T, ctx context.Context, tx pgx.Tx, role, migration, label string) {
	t.Helper()
	if role != "" {
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
			t.Fatalf("%s: set role: %v", label, err)
		}
	}
	_, migrationErr := tx.Exec(ctx, migration)
	if role != "" {
		if _, err := tx.Exec(ctx, "RESET ROLE"); err != nil && migrationErr == nil {
			t.Fatalf("%s: reset role: %v", label, err)
		}
	}
	if migrationErr != nil {
		t.Fatalf("%s: %v", label, migrationErr)
	}
}

func assertRLSForced(t *testing.T, ctx context.Context, tx pgx.Tx) {
	t.Helper()
	var enabled, forced bool
	if err := tx.QueryRow(ctx, `
SELECT relrowsecurity, relforcerowsecurity
FROM pg_class
WHERE oid = 'bots'::regclass
`).Scan(&enabled, &forced); err != nil {
		t.Fatalf("read bots RLS state: %v", err)
	}
	if !enabled || !forced {
		t.Fatalf("bots RLS state = enabled %t forced %t, want both restored", enabled, forced)
	}
}

func assertNullableInt(t *testing.T, ctx context.Context, tx pgx.Tx, id int, want *int) {
	t.Helper()
	var got *int
	if err := tx.QueryRow(ctx, "SELECT compaction_target_percent FROM bots WHERE id = $1", id).Scan(&got); err != nil {
		t.Fatalf("read target for bot %d: %v", id, err)
	}
	if got == nil || want == nil {
		if got != nil || want != nil {
			t.Fatalf("target for bot %d = %v, want %v", id, got, want)
		}
		return
	}
	if *got != *want {
		t.Fatalf("target for bot %d = %d, want %d", id, *got, *want)
	}
}

func assertInt(t *testing.T, ctx context.Context, tx pgx.Tx, id, want int) {
	t.Helper()
	var got int
	if err := tx.QueryRow(ctx, "SELECT compaction_ratio FROM bots WHERE id = $1", id).Scan(&got); err != nil {
		t.Fatalf("read ratio for bot %d: %v", id, err)
	}
	if got != want {
		t.Fatalf("ratio for bot %d = %d, want %d", id, got, want)
	}
}

func intPointer(value int) *int {
	return &value
}
