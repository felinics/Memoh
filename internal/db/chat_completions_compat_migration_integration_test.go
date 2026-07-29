//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/team"
)

func TestExplicitChatCompletionsCompatMigrationBackfillAndRollback(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := pool.Config().ConnString()

	// Seed the pre-0123 state.
	stepDown(t, dsn, 1)

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT set_config('memoh.team_id', $1, false)", team.DefaultTeamID); err != nil {
		t.Fatalf("set team context: %v", err)
	}

	moonshotTemplateID := uuid.NewString()
	if _, err := conn.Exec(ctx, `
		INSERT INTO template.provider_templates (
			id, key, domain, name, driver, source, content_hash
		) VALUES ($1, 'moonshot', 'llm', 'Moonshot', 'openai-completions', 'moonshot.yaml', 'test-moonshot')
	`, moonshotTemplateID); err != nil {
		t.Fatalf("insert Moonshot template: %v", err)
	}

	type seed struct {
		name       string
		templateID *string
		config     string
		metadata   string
	}
	seeds := []seed{
		{
			name:       "Kimi linked template",
			templateID: &moonshotTemplateID,
			config:     `{"base_url":"https://proxy.example/v1"}`,
			metadata:   `{}`,
		},
		{
			name:     "DeepSeek registry source",
			config:   `{"base_url":"https://proxy.example/v1"}`,
			metadata: `{"registry":{"source":"deepseek.yaml"}}`,
		},
		{
			name:     "MiniMax canonical endpoint",
			config:   `{"base_url":"https://api.minimaxi.com/v1/"}`,
			metadata: `{}`,
		},
		{
			name:     "DeepSeek beta endpoint",
			config:   `{"base_url":"https://api.deepseek.com/beta"}`,
			metadata: `{}`,
		},
		{
			name:     "Lookalike domain",
			config:   `{"base_url":"https://api.deepseek.com.evil.example/v1"}`,
			metadata: `{}`,
		},
		{
			name:     "Existing explicit value",
			config:   `{"base_url":"https://api.moonshot.cn/v1","chat_completions_compat":"deepseek"}`,
			metadata: `{}`,
		},
		{
			name:     "Unknown provider",
			config:   `{"base_url":"https://proxy.example/v1"}`,
			metadata: `{}`,
		},
	}
	for _, item := range seeds {
		if _, err := conn.Exec(ctx, `
			INSERT INTO public.providers (
				id, provider_template_id, name, client_type, config, metadata
			) VALUES ($1, $2, $3, 'openai-completions', $4::jsonb, $5::jsonb)
		`, uuid.NewString(), item.templateID, item.name, item.config, item.metadata); err != nil {
			t.Fatalf("insert provider %q: %v", item.name, err)
		}
	}

	stepUp(t, dsn, 1)

	assertProviderCompat := func(name, wantCompat, wantMarker string) {
		t.Helper()
		var compat, marker pgtype.Text
		if err := conn.QueryRow(ctx, `
			SELECT
				config ->> 'chat_completions_compat',
				metadata ->> '_memoh_migration_0123_chat_completions_compat'
			FROM public.providers
			WHERE name = $1
		`, name).Scan(&compat, &marker); err != nil {
			t.Fatalf("read provider %q: %v", name, err)
		}
		if compat.String != wantCompat || compat.Valid != (wantCompat != "") {
			t.Fatalf("provider %q compat = %#v, want %q", name, compat, wantCompat)
		}
		if marker.String != wantMarker || marker.Valid != (wantMarker != "") {
			t.Fatalf("provider %q marker = %#v, want %q", name, marker, wantMarker)
		}
	}

	assertProviderCompat("Kimi linked template", "kimi", "kimi")
	assertProviderCompat("DeepSeek registry source", "deepseek", "deepseek")
	assertProviderCompat("MiniMax canonical endpoint", "minimax", "minimax")
	assertProviderCompat("DeepSeek beta endpoint", "deepseek", "deepseek")
	assertProviderCompat("Lookalike domain", "", "")
	assertProviderCompat("Existing explicit value", "deepseek", "")
	assertProviderCompat("Unknown provider", "", "")

	// A user edit made after the migration must survive rollback.
	if _, err := conn.Exec(ctx, `
		UPDATE public.providers
		SET config = jsonb_set(config, '{chat_completions_compat}', '"custom"'::jsonb)
		WHERE name = 'DeepSeek registry source'
	`); err != nil {
		t.Fatalf("customize migrated provider: %v", err)
	}

	stepDown(t, dsn, 1)
	assertProviderCompat("Kimi linked template", "", "")
	assertProviderCompat("DeepSeek registry source", "custom", "")
	assertProviderCompat("MiniMax canonical endpoint", "", "")
	assertProviderCompat("DeepSeek beta endpoint", "", "")
	assertProviderCompat("Lookalike domain", "", "")
	assertProviderCompat("Existing explicit value", "deepseek", "")
	assertProviderCompat("Unknown provider", "", "")

	// Leave the isolated database at the current schema version.
	stepUp(t, dsn, 1)
}
