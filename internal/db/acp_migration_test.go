package db

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	embeddeddb "github.com/memohai/memoh/db"
)

func TestPostgresACPAgentSessionTypeMigrationFiles(t *testing.T) {
	baseline := readEmbeddedMigration(t, "postgres/legacy/v1/migrations/0001_init.up.sql")
	if !strings.Contains(baseline, "type IN ('chat', 'heartbeat', 'schedule', 'subagent', 'discuss', 'acp_agent')") {
		t.Fatal("postgres baseline bot_sessions type CHECK missing acp_agent")
	}
	up := readEmbeddedMigration(t, "postgres/legacy/v1/migrations/0082_acp_agent_session_type.up.sql")
	if !strings.Contains(up, "DROP CONSTRAINT IF EXISTS bot_sessions_type_check") ||
		!strings.Contains(up, "'acp_agent'") {
		t.Fatal("postgres 0082 up migration does not widen bot_sessions_type_check to acp_agent")
	}
	down := readEmbeddedMigration(t, "postgres/legacy/v1/migrations/0082_acp_agent_session_type.down.sql")
	if !strings.Contains(down, "WHERE type = 'acp_agent'") ||
		!strings.Contains(down, "RAISE EXCEPTION") {
		t.Fatal("postgres 0082 down migration must guard existing acp_agent rows without touching tool approvals")
	}
}

func TestToolApprovalRequestsConstrainOperationNotToolName(t *testing.T) {
	path := "postgres/legacy/v1/migrations/0001_init.up.sql"
	sql := readEmbeddedMigration(t, path)
	tableSQL := toolApprovalTableSQL(sql)
	const operationColumn = "operation TEXT NOT NULL"
	if !strings.Contains(tableSQL, operationColumn) {
		t.Fatalf("%s missing tool approval operation column %q", path, operationColumn)
	}
	const operationCheck = "CHECK (operation IN ('read', 'write', 'exec'))"
	if !strings.Contains(tableSQL, operationCheck) {
		t.Fatalf("%s missing Memoh-native tool approval operation CHECK %q", path, operationCheck)
	}
	if strings.Contains(tableSQL, "CHECK (tool_name IN") {
		t.Fatalf("%s tool_approval_requests must not constrain real tool_name values", path)
	}
	if strings.Contains(tableSQL, "acp_agent") {
		t.Fatalf("%s tool_approval_requests CHECK must not include ACP tools", path)
	}
}

func toolApprovalTableSQL(sql string) string {
	start := strings.Index(sql, "CREATE TABLE IF NOT EXISTS tool_approval_requests")
	if start < 0 {
		return ""
	}
	tail := sql[start:]
	end := strings.Index(tail, "CREATE INDEX")
	if end < 0 {
		return tail
	}
	return tail[:end]
}

func readEmbeddedMigration(t *testing.T, path string) string {
	t.Helper()
	data, err := embeddeddb.MigrationsFS.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration %s: %v", path, err)
	}
	return string(data)
}

// readEmbeddedPreTeamInit returns the canonical table definitions before the
// Team finalizer. Tests that execute 0001 inside a temporary search_path use
// this historical fixture because the Team schema intentionally targets the
// real public schema.
func readEmbeddedPreTeamInit(t *testing.T) string {
	t.Helper()
	const marker = "-- Canonical team and membership schema"
	sql := readEmbeddedMigration(t, "postgres/legacy/v1/migrations/0001_init.up.sql")
	before, _, ok := strings.Cut(sql, marker)
	if !ok {
		t.Fatalf("canonical 0001 is missing %q", marker)
	}
	return before
}

// applyPreTeamBaseline replays the historical 0001 tables into the temporary
// schema the caller has already put at the head of its search_path.
//
// The baseline guards its enum with an unqualified pg_type lookup, which finds
// iam.user_role in an Epoch v2 database and therefore skips the CREATE.
// Pre-creating the type in the temporary schema is what keeps that skip
// harmless: without it the guard passes silently and the first table declaring a
// user_role column cannot resolve the type, since platform is not on the
// search_path.
func applyPreTeamBaseline(t *testing.T, ctx context.Context, tx pgx.Tx) {
	t.Helper()
	if _, err := tx.Exec(ctx, "CREATE TYPE user_role AS ENUM ('member', 'admin')"); err != nil {
		t.Fatalf("pre-create user_role: %v", err)
	}
	if _, err := tx.Exec(ctx, readEmbeddedPreTeamInit(t)); err != nil {
		t.Fatalf("apply pre-team schema fixture: %v", err)
	}
}
