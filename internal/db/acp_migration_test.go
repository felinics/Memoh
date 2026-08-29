package db

import (
	"strings"
	"testing"

	embeddeddb "github.com/felinics/memoh/db"
)

func TestPostgresACPAgentSessionTypeMigrationFiles(t *testing.T) {
	baseline := readEmbeddedMigration(t, "postgres/migrations/0001_init.up.sql")
	if !strings.Contains(baseline, "type IN ('chat', 'schedule', 'subagent', 'discuss', 'acp_agent')") {
		t.Fatal("postgres baseline bot_sessions type CHECK missing acp_agent")
	}
	up := readEmbeddedMigration(t, "postgres/migrations/0082_acp_agent_session_type.up.sql")
	if !strings.Contains(up, "DROP CONSTRAINT IF EXISTS bot_sessions_type_check") ||
		!strings.Contains(up, "'acp_agent'") {
		t.Fatal("postgres 0082 up migration does not widen bot_sessions_type_check to acp_agent")
	}
	down := readEmbeddedMigration(t, "postgres/migrations/0082_acp_agent_session_type.down.sql")
	if !strings.Contains(down, "WHERE type = 'acp_agent'") ||
		!strings.Contains(down, "RAISE EXCEPTION") {
		t.Fatal("postgres 0082 down migration must guard existing acp_agent rows without touching tool approvals")
	}
}

func TestToolApprovalMigrationContracts(t *testing.T) {
	path := "postgres/migrations/0001_init.up.sql"
	sql := readEmbeddedMigration(t, path)
	tableSQL := toolApprovalTableSQL(sql)
	const operationColumn = "operation TEXT NOT NULL"
	if !strings.Contains(tableSQL, operationColumn) {
		t.Fatalf("%s missing tool approval operation column %q", path, operationColumn)
	}
	const operationCheck = "CHECK (operation IN ('read', 'write', 'exec', 'permission'))"
	if !strings.Contains(tableSQL, operationCheck) {
		t.Fatalf("%s missing Memoh-native tool approval operation CHECK %q", path, operationCheck)
	}
	if strings.Contains(tableSQL, "CHECK (tool_name IN") {
		t.Fatalf("%s tool_approval_requests must not constrain real tool_name values", path)
	}
	if strings.Contains(tableSQL, "acp_agent") {
		t.Fatalf("%s tool_approval_requests CHECK must not include ACP tools", path)
	}

	upSQL := readEmbeddedMigration(t, "postgres/migrations/0131_tool_approval_options.up.sql")
	for _, contract := range []string{
		"ADD COLUMN IF NOT EXISTS options JSONB NOT NULL DEFAULT '[]'::jsonb",
		"ADD COLUMN IF NOT EXISTS selected_option_id TEXT NOT NULL DEFAULT ''",
		"VALIDATE CONSTRAINT tool_approval_operation_check_0131",
	} {
		if !strings.Contains(upSQL, contract) {
			t.Fatalf("0131 up missing %q", contract)
		}
	}
	downSQL := readEmbeddedMigration(t, "postgres/migrations/0131_tool_approval_options.down.sql")
	for _, guard := range []string{
		"CHECK (operation IN ('read', 'write', 'exec')) NOT VALID",
		"CHECK (options = '[]'::jsonb AND selected_option_id = '') NOT VALID",
		"RAISE EXCEPTION",
	} {
		if !strings.Contains(downSQL, guard) {
			t.Fatalf("0131 down must fail closed on audit data; missing %q", guard)
		}
	}
	if strings.Contains(downSQL, "DISABLE ROW LEVEL SECURITY") ||
		strings.Contains(downSQL, "NO FORCE ROW LEVEL SECURITY") {
		t.Fatal("0131 down must not disable FORCE RLS")
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
	sql := readEmbeddedMigration(t, "postgres/migrations/0001_init.up.sql")
	before, _, ok := strings.Cut(sql, marker)
	if !ok {
		t.Fatalf("canonical 0001 is missing %q", marker)
	}
	return before
}
