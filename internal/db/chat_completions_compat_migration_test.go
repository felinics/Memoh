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
		"https://api.deepseek.com/v1",
		"https://api.minimaxi.com/v1",
		"https://api.moonshot.cn/v1",
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

	down := readEmbeddedMigration(t, "postgres/migrations/0123_explicit_chat_completions_compat.down.sql")
	if !strings.Contains(down, marker) ||
		!strings.Contains(down, "config ->> 'chat_completions_compat' = metadata ->> marker_key") {
		t.Fatal("0123 down migration must remove only unchanged values marked by its up migration")
	}
}
