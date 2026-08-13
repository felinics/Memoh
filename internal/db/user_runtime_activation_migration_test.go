package db

import (
	"strings"
	"testing"
)

func TestRemoteMountDefaultMigrationDoesNotRewriteExistingRows(t *testing.T) {
	up := readEmbeddedMigration(t, "postgres/migrations/0132_remote_mount_default_allow.up.sql")
	if !strings.Contains(up, "ALTER COLUMN tool_approval_config SET DEFAULT") {
		t.Fatal("0132 up must change the default for new Remote Runtime mounts")
	}
	if strings.Contains(strings.ToUpper(up), "UPDATE BOT_REMOTE_RUNTIME_BINDINGS") {
		t.Fatal("0132 up must not overwrite existing mount approval choices")
	}

	down := readEmbeddedMigration(t, "postgres/migrations/0132_remote_mount_default_allow.down.sql")
	if !strings.Contains(down, `"write":{"mode":"ask"`) ||
		!strings.Contains(down, `"exec":{"mode":"ask"`) {
		t.Fatal("0132 down must restore the previous mount-time default")
	}
}

func TestUserRuntimeActivationLifecycleMigrationContract(t *testing.T) {
	baseline := readEmbeddedMigration(t, "postgres/migrations/0001_init.up.sql")
	userRuntimes := migrationTableSQL(baseline, "user_runtimes")
	for _, contract := range []string{
		"activated_at TIMESTAMPTZ",
		"pending_expires_at TIMESTAMPTZ DEFAULT (now() + INTERVAL '15 minutes')",
		"CONSTRAINT user_runtimes_activation_state_check",
	} {
		if !strings.Contains(userRuntimes, contract) {
			t.Fatalf("canonical user_runtimes schema missing %q", contract)
		}
	}

	up := readEmbeddedMigration(t, "postgres/migrations/0133_user_runtime_activation_lifecycle.up.sql")
	for _, contract := range []string{
		"ADD COLUMN IF NOT EXISTS activated_at",
		"ADD COLUMN IF NOT EXISTS pending_expires_at",
		"SET activated_at = created_at",
		"ALTER COLUMN pending_expires_at SET DEFAULT (now() + INTERVAL '15 minutes')",
		"VALIDATE CONSTRAINT user_runtimes_activation_state_check",
	} {
		if !strings.Contains(up, contract) {
			t.Fatalf("0133 up missing %q", contract)
		}
	}

	down := readEmbeddedMigration(t, "postgres/migrations/0133_user_runtime_activation_lifecycle.down.sql")
	for _, contract := range []string{
		"DROP CONSTRAINT IF EXISTS user_runtimes_activation_state_check",
		"DROP COLUMN IF EXISTS pending_expires_at",
		"DROP COLUMN IF EXISTS activated_at",
	} {
		if !strings.Contains(down, contract) {
			t.Fatalf("0133 down missing %q", contract)
		}
	}
}

func migrationTableSQL(sql, table string) string {
	start := strings.Index(sql, "CREATE TABLE IF NOT EXISTS "+table)
	if start < 0 {
		return ""
	}
	tail := sql[start:]
	end := strings.Index(tail, "\n);")
	if end < 0 {
		return tail
	}
	return tail[:end+3]
}
