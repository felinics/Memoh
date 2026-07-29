package db

import (
	"strings"
	"testing"
)

func TestExplicitChatCompletionsCompatMigrationFiles(t *testing.T) {
	t.Parallel()

	const marker = "_memoh_migration_0123_chat_completions_compat"
	up := readEmbeddedMigration(t, "postgres/migrations/0123_explicit_chat_completions_compat.up.sql")
	for _, fragment := range []string{
		"provider_template_id",
		"'{template,key}'",
		"'{registry,source}'",
		"base_url = 'https://api.deepseek.com'",
		"base_url LIKE 'https://api.deepseek.com/%'",
		"base_url LIKE 'https://api.minimax.io/%'",
		"base_url LIKE 'https://api.minimaxi.com/%'",
		"base_url LIKE 'https://api.moonshot.cn/%'",
		"base_url LIKE 'https://api.moonshot.ai/%'",
		"THEN 'deepseek'",
		"THEN 'minimax'",
		"THEN 'kimi'",
		marker,
	} {
		if !strings.Contains(up, fragment) {
			t.Fatalf("0123 up migration is missing %q", fragment)
		}
	}
	if strings.Contains(up, "provider.name") {
		t.Fatal("0123 must not infer compatibility from a user-editable provider name")
	}
	if strings.Contains(up, "LIKE '%") {
		t.Fatal("0123 must match official endpoints by origin prefix, not substring")
	}

	down := readEmbeddedMigration(t, "postgres/migrations/0123_explicit_chat_completions_compat.down.sql")
	if !strings.Contains(down, marker) ||
		!strings.Contains(down, "config ->> 'chat_completions_compat' = metadata ->> marker_key") {
		t.Fatal("0123 down migration must remove only unchanged values marked by its up migration")
	}
}
