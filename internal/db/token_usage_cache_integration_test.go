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

func TestTokenUsageCacheSemanticsPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	schema := pgx.Identifier{"usage_cache_" + strings.ReplaceAll(uuid.NewString(), "-", "")}.Sanitize()
	if _, err := tx.Exec(ctx, "CREATE SCHEMA "+schema+"; SET LOCAL search_path TO "+schema+", public"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, readEmbeddedPreTeamInit(t)); err != nil {
		t.Fatal(err)
	}
	bindTeamQueryFixture(t, ctx, tx)
	if _, err := tx.Exec(ctx, `
ALTER TABLE bot_sessions ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
ALTER TABLE bot_history_messages ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
ALTER TABLE providers ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
ALTER TABLE models ADD COLUMN team_id UUID NOT NULL DEFAULT public.memoh_current_team_id();
DROP VIEW bot_visible_history_messages;
CREATE VIEW bot_visible_history_messages AS SELECT * FROM bot_history_messages;
`); err != nil {
		t.Fatal(err)
	}
	userID, botID := uuid.NewString(), uuid.NewString()
	if _, err := tx.Exec(ctx, "INSERT INTO users (id) VALUES ($1)", userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO bots (id, owner_user_id, type, name) VALUES ($1, $2, 'personal', 'usage-test')", botID, userID); err != nil {
		t.Fatal(err)
	}
	const anthropic = `{"context_lifecycle":{"client_type":"anthropic-messages"}}`
	cases := []struct {
		name, runtime, metadata, usage string
		want                           int64
	}{
		{"legacy read and write", "model", anthropic, `{"inputTokens":10,"inputTokenDetails":{"cacheReadTokens":200,"cacheWriteTokens":100,"cacheWrite5mTokens":100}}`, 310},
		{"normalized", "model", anthropic, `{"inputTokenSemantics":"total","inputTokens":310,"inputTokenDetails":{"cacheReadTokens":200,"cacheWriteTokens":100}}`, 310},
		{"legacy all cached", "model", anthropic, `{"inputTokens":0,"inputTokenDetails":{"cacheReadTokens":200}}`, 200},
		{"normalized all cached", "model", anthropic, `{"inputTokenSemantics":"total","inputTokens":200,"inputTokenDetails":{"cacheReadTokens":200}}`, 200},
		{"openai", "model", `{"context_lifecycle":{"client_type":"openai-responses"}}`, `{"inputTokens":310,"inputTokenDetails":{"cacheReadTokens":200}}`, 310},
		{"acp", "acp_agent", anthropic, `{"inputTokens":310,"inputTokenDetails":{"cacheReadTokens":200}}`, 310},
		{"codex", "codex", anthropic, `{"inputTokens":310,"inputTokenDetails":{"cacheReadTokens":200}}`, 310},
		{"unknown protocol", "model", `{}`, `{"inputTokens":10,"inputTokenDetails":{"cacheReadTokens":200}}`, 10},
		{"future semantics", "model", anthropic, `{"inputTokenSemantics":"future","inputTokens":310,"inputTokenDetails":{"cacheReadTokens":200}}`, 310},
	}
	queries := sqlc.New(tx)
	wantRecords := make(map[string]int64)
	var wantTotal int64
	for _, tc := range cases {
		sessionID := uuid.NewString()
		if _, err := tx.Exec(ctx, "INSERT INTO bot_sessions (id, bot_id) VALUES ($1, $2)", sessionID, botID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO bot_history_messages
 (bot_id, session_id, role, content, usage, metadata, runtime_type)
 VALUES ($1, $2, 'assistant', '"fixture"', $3, $4, $5)`, botID, sessionID, tc.usage, tc.metadata, tc.runtime); err != nil {
			t.Fatal(err)
		}
		id, _ := ParseUUID(sessionID)
		for range 2 {
			row, err := queries.GetSessionCacheStats(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if row.TotalInputTokens != tc.want || row.CacheReadTokens != 200 {
				t.Fatalf("%s: cache stats = %+v, want input %d/read 200", tc.name, row, tc.want)
			}
			latest, err := queries.GetLatestAssistantUsage(ctx, id)
			if err != nil || latest != tc.want {
				t.Fatalf("%s: latest input = %d, err = %v, want %d", tc.name, latest, err, tc.want)
			}
		}
		wantRecords[sessionID] = tc.want
		wantTotal += tc.want
	}
	botUUID, _ := ParseUUID(botID)
	from := pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}
	to := pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}
	records, err := queries.ListTokenUsageRecords(ctx, sqlc.ListTokenUsageRecordsParams{BotID: botUUID, FromTime: from, ToTime: to, PageLimit: 100})
	if err != nil || len(records) != len(cases) {
		t.Fatalf("records = %d, err = %v", len(records), err)
	}
	for _, row := range records {
		if want := wantRecords[uuid.UUID(row.SessionID.Bytes).String()]; row.InputTokens != want {
			t.Errorf("record input = %d, want %d", row.InputTokens, want)
		}
	}
	days, err := queries.GetTokenUsageByDayAndType(ctx, sqlc.GetTokenUsageByDayAndTypeParams{BotID: botUUID, FromTime: from, ToTime: to})
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, day := range days {
		total += day.InputTokens
	}
	if total != wantTotal {
		t.Errorf("daily input = %d, want %d", total, wantTotal)
	}
	models, err := queries.GetTokenUsageByModel(ctx, sqlc.GetTokenUsageByModelParams{BotID: botUUID, FromTime: from, ToTime: to})
	if err != nil || len(models) != 1 || models[0].InputTokens != wantTotal {
		t.Fatalf("model totals = %+v, err = %v, want %d", models, err, wantTotal)
	}
	var rawInput int64
	if err := tx.QueryRow(ctx, "SELECT sum((usage->>'inputTokens')::bigint) FROM bot_history_messages").Scan(&rawInput); err != nil || rawInput != 1770 {
		t.Fatalf("raw input changed: %d, err = %v", rawInput, err)
	}
}
