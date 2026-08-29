package db

import (
	"strings"
	"testing"
)

func TestBotWorkdirRemoteBindingDeletePolicyMigrations(t *testing.T) {
	t.Parallel()

	baseline := botWorkdirsTableSQL(readEmbeddedMigration(t, "postgres/migrations/0001_init.up.sql"))
	if !strings.Contains(baseline, "CONSTRAINT bot_workdirs_remote_binding_fkey") ||
		!strings.Contains(baseline, "REFERENCES public.bot_remote_runtime_bindings(team_id, id) ON DELETE RESTRICT") {
		t.Fatal("canonical bot_workdirs schema must restrict deletion of referenced remote bindings")
	}
	if strings.Contains(baseline, "REFERENCES public.bot_remote_runtime_bindings(team_id, id) ON DELETE CASCADE") {
		t.Fatal("canonical bot_workdirs schema still cascades remote binding deletion")
	}

	// 0129 is already published and must remain byte-semantically historical;
	// deployed databases receive the policy change only through 0142.
	published := readEmbeddedMigration(t, "postgres/migrations/0129_bot_workdirs.up.sql")
	if !strings.Contains(published, "REFERENCES public.bot_remote_runtime_bindings(team_id, id) ON DELETE CASCADE") {
		t.Fatal("published 0129 migration no longer contains its original CASCADE constraint")
	}

	up := readEmbeddedMigration(t, "postgres/migrations/0142_bot_workdirs_remote_binding_restrict.up.sql")
	if !strings.HasPrefix(up, "-- 0142_bot_workdirs_remote_binding_restrict\n") ||
		!strings.Contains(up, "DROP CONSTRAINT IF EXISTS bot_workdirs_remote_binding_fkey") ||
		!strings.Contains(up, "ON DELETE RESTRICT") {
		t.Fatal("0142 up migration must replace the published foreign key with RESTRICT")
	}
	if strings.Contains(up, "ON DELETE CASCADE") || !strings.Contains(up, "NOT VALID") {
		t.Fatal("0142 up migration must install an RLS-safe NOT VALID RESTRICT constraint")
	}

	down := readEmbeddedMigration(t, "postgres/migrations/0142_bot_workdirs_remote_binding_restrict.down.sql")
	if !strings.HasPrefix(down, "-- 0142_bot_workdirs_remote_binding_restrict\n") ||
		!strings.Contains(down, "DROP CONSTRAINT IF EXISTS bot_workdirs_remote_binding_fkey") ||
		!strings.Contains(down, "ON DELETE CASCADE") {
		t.Fatal("0142 down migration must restore the published CASCADE constraint")
	}
	if !strings.Contains(down, "NOT VALID") {
		t.Fatal("0142 down migration must restore an RLS-safe NOT VALID CASCADE constraint")
	}
}

func botWorkdirsTableSQL(sql string) string {
	start := strings.Index(sql, "CREATE TABLE IF NOT EXISTS public.bot_workdirs")
	if start < 0 {
		return ""
	}
	tail := sql[start:]
	end := strings.Index(tail, "CREATE UNIQUE INDEX IF NOT EXISTS bot_workdirs_target_path_unique")
	if end < 0 {
		return tail
	}
	return tail[:end]
}
