package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// TestTokenUsageModelNameFallsBackToModelID exercises the real SQL label
// fallback against PostgreSQL: a model whose display name was never set must
// surface its model_id as model_name in the token-usage queries, instead of
// the literal 'Unknown' that would defeat the frontend's slug fallback. Only
// messages whose model row is gone (model_id NULL) stay 'Unknown'.
func TestTokenUsageModelNameFallsBackToModelID(t *testing.T) {
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
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	schema := "token_usage_label_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+quotedSchema+", public"); err != nil {
		t.Fatalf("set search path: %v", err)
	}
	baseline := readEmbeddedPreTeamInit(t)
	if _, err := tx.Exec(ctx, baseline); err != nil {
		t.Fatalf("apply 0001 baseline: %v", err)
	}
	bindTeamQueryFixture(t, ctx, tx)
	if _, err := tx.Exec(ctx, `
ALTER TABLE bot_sessions ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
ALTER TABLE bot_history_messages ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
ALTER TABLE providers ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
ALTER TABLE models ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
`); err != nil {
		t.Fatalf("add team query fixture schema: %v", err)
	}

	userID := uuid.NewString()
	botID := uuid.NewString()
	sessionID := uuid.NewString()
	providerID := uuid.NewString()
	namedModelID := uuid.NewString()
	namelessModelID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bots (id, owner_user_id, type, name) VALUES ($1, $2, 'personal', 'label-test')`, botID, userID); err != nil {
		t.Fatalf("insert bot: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bot_sessions (id, bot_id) VALUES ($1, $2)`, sessionID, botID); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO providers (id, name) VALUES ($1, 'acme')`, providerID); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO models (id, model_id, name, provider_id) VALUES
  ($1, 'claude-fable-5', 'Fable', $3),
  ($2, 'gpt-5.4-mini', NULL, $3)
`, namedModelID, namelessModelID, providerID); err != nil {
		t.Fatalf("insert models: %v", err)
	}
	for _, modelID := range []any{namedModelID, namelessModelID, nil} {
		if _, err := tx.Exec(ctx, `
INSERT INTO bot_history_messages (bot_id, session_id, role, content, usage, model_id)
VALUES ($1, $2, 'assistant', '[{"type":"text","text":"m"}]', '{"inputTokens": 10, "outputTokens": 5}', $3)
`, botID, sessionID, modelID); err != nil {
			t.Fatalf("insert message for model %v: %v", modelID, err)
		}
	}

	var botUUID pgtype.UUID
	if err := botUUID.Scan(botID); err != nil {
		t.Fatalf("scan bot uuid: %v", err)
	}
	from := pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}
	to := pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}

	wantNames := map[string]string{
		"claude-fable-5": "Fable",
		"gpt-5.4-mini":   "gpt-5.4-mini",
		"unknown":        "Unknown",
	}

	byModel, err := sqlc.New(tx).GetTokenUsageByModel(ctx, sqlc.GetTokenUsageByModelParams{
		BotID:    botUUID,
		FromTime: from,
		ToTime:   to,
	})
	if err != nil {
		t.Fatalf("GetTokenUsageByModel: %v", err)
	}
	if len(byModel) != len(wantNames) {
		t.Fatalf("GetTokenUsageByModel rows = %d, want %d: %+v", len(byModel), len(wantNames), byModel)
	}
	for _, row := range byModel {
		if want, ok := wantNames[row.ModelSlug]; !ok {
			t.Errorf("GetTokenUsageByModel unexpected slug %q", row.ModelSlug)
		} else if row.ModelName != want {
			t.Errorf("GetTokenUsageByModel model_name for slug %q = %q, want %q", row.ModelSlug, row.ModelName, want)
		}
	}

	records, err := sqlc.New(tx).ListTokenUsageRecords(ctx, sqlc.ListTokenUsageRecordsParams{
		BotID:     botUUID,
		FromTime:  from,
		ToTime:    to,
		PageLimit: 10,
	})
	if err != nil {
		t.Fatalf("ListTokenUsageRecords: %v", err)
	}
	if len(records) != len(wantNames) {
		t.Fatalf("ListTokenUsageRecords rows = %d, want %d: %+v", len(records), len(wantNames), records)
	}
	for _, row := range records {
		if want, ok := wantNames[row.ModelSlug]; !ok {
			t.Errorf("ListTokenUsageRecords unexpected slug %q", row.ModelSlug)
		} else if row.ModelName != want {
			t.Errorf("ListTokenUsageRecords model_name for slug %q = %q, want %q", row.ModelSlug, row.ModelName, want)
		}
	}
}
