package application_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/felinics/twilight/sdk"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	message "github.com/felinics/memoh/internal/chat/message"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/models"
)

const (
	postgresMessageTestUserID    = "61000000-0000-4000-8000-000000000001"
	postgresMessageTestBotID     = "61000000-0000-4000-8000-000000000002"
	postgresMessageTestSessionID = "61000000-0000-4000-8000-000000000003"
)

func beginUsagePostgresTx(t *testing.T, ctx context.Context) pgx.Tx {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	return tx
}

func setupUsagePostgresFixtures(t *testing.T, ctx context.Context, tx pgx.Tx) {
	t.Helper()
	name := "usage-" + uuid.NewString()
	if _, err := tx.Exec(ctx, `WITH u AS (INSERT INTO users(id,username,is_active) VALUES($1,$2,true) RETURNING id) INSERT INTO team_members(user_id,role) SELECT id,'admin' FROM u`, postgresMessageTestUserID, name); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bots(id,owner_user_id,name) VALUES($1,$2,$3)`, postgresMessageTestBotID, postgresMessageTestUserID, name); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bot_sessions(id,bot_id,channel_type) VALUES($1,$2,'local')`, postgresMessageTestSessionID, postgresMessageTestBotID); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresCacheReportingPreservesUnknownRecords(t *testing.T) {
	for _, tt := range []struct {
		name     string
		rows     []string
		reported bool
	}{
		{"explicit zero", []string{`{"inputTokens":100,"cacheReadTokensReported":true,"inputTokenDetails":{"cacheReadTokens":0}}`}, true},
		{"unreported positive aggregate", []string{`{"inputTokens":1000,"cacheReadTokensReported":false,"inputTokenDetails":{"cacheReadTokens":10}}`}, false},
		{"legacy zero", []string{`{"inputTokens":100,"inputTokenDetails":{"cacheReadTokens":0}}`}, false},
		{"legacy positive", []string{`{"inputTokens":10,"inputTokenDetails":{"cacheReadTokens":200}}`}, false},
		{"mixed records", []string{`{"inputTokens":100,"cacheReadTokensReported":true,"inputTokenDetails":{"cacheReadTokens":10}}`, `{"inputTokens":900,"cacheReadTokensReported":false,"inputTokenDetails":{"cacheReadTokens":0}}`}, false},
		{"missing usage contribution", []string{`{"inputTokens":100,"cacheReadTokensReported":true,"inputTokenDetails":{"cacheReadTokens":10}}`, `{}`}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			tx := beginUsagePostgresTx(t, ctx)
			setupUsagePostgresFixtures(t, ctx, tx)
			queries := sqlc.New(tx)
			svc := message.NewService(nil, postgresstore.NewQueries(queries))
			for _, raw := range tt.rows {
				_, err := svc.Persist(ctx, message.PersistInput{BotID: postgresMessageTestBotID, SessionID: postgresMessageTestSessionID, Role: "assistant", Content: json.RawMessage(`[{"type":"text","text":"ok"}]`), Usage: json.RawMessage(raw), RuntimeType: "model"})
				if err != nil {
					t.Fatal(err)
				}
			}
			botID, _ := dbpkg.ParseUUID(postgresMessageTestBotID)
			rows, err := queries.GetTokenUsageByDayAndType(ctx, sqlc.GetTokenUsageByDayAndTypeParams{BotID: botID, FromTime: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}, ToTime: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
			if err != nil || len(rows) != 1 || rows[0].CacheReadTokensReported != tt.reported {
				t.Fatalf("daily=%+v err=%v want reported=%t", rows, err, tt.reported)
			}
			records, err := queries.ListTokenUsageRecords(ctx, sqlc.ListTokenUsageRecordsParams{BotID: botID, FromTime: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}, ToTime: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, PageLimit: 10})
			if err != nil || len(records) != len(tt.rows) {
				t.Fatalf("records=%+v err=%v", records, err)
			}
			reported := 0
			for _, record := range records {
				if record.CacheReadTokensReported {
					reported++
				}
			}
			if want := strings.Count(strings.Join(tt.rows, ""), `"cacheReadTokensReported":true`); reported != want {
				t.Fatalf("records=%+v want %d reported", records, want)
			}
		})
	}
}

func TestPostgresRemovedModelKeepsUsageWithoutReattribution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx := beginUsagePostgresTx(t, ctx)
	setupUsagePostgresFixtures(t, ctx, tx)
	providerID, modelID, replacementID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO providers(id,name,client_type) VALUES($1,'fixture','openai-responses')`, providerID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO models(id,provider_id,model_id,name) VALUES($1,$2,'fixture-model','Fixture')`, modelID, providerID); err != nil {
		t.Fatal(err)
	}
	queries := sqlc.New(tx)
	store := postgresstore.NewQueries(queries)
	svc := message.NewService(nil, store)
	raw := json.RawMessage(`{"inputTokens":1000,"outputTokens":200,"cacheReadTokensReported":true,"inputTokenDetails":{"cacheReadTokens":800}}`)
	saved, err := svc.Persist(ctx, message.PersistInput{BotID: postgresMessageTestBotID, SessionID: postgresMessageTestSessionID, Role: "assistant", Content: json.RawMessage(`[{"type":"text","text":"ok"}]`), ModelID: modelID, Usage: raw, RuntimeType: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if err := models.NewService(slog.Default(), store).DeleteByID(ctx, modelID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO models(id,provider_id,model_id,name) VALUES($1,$2,'fixture-model','Fixture')`, replacementID, providerID); err != nil {
		t.Fatal(err)
	}
	botID, _ := dbpkg.ParseUUID(postgresMessageTestBotID)
	replacementUUID, _ := dbpkg.ParseUUID(replacementID)
	from := pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}
	to := pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}
	for range 2 {
		byModel, err := queries.GetTokenUsageByModel(ctx, sqlc.GetTokenUsageByModelParams{BotID: botID, FromTime: from, ToTime: to})
		if err != nil || len(byModel) != 1 || byModel[0].ModelID.Valid || byModel[0].ModelName != "Unknown" || byModel[0].InputTokens != 1000 || byModel[0].OutputTokens != 200 {
			t.Fatalf("model attribution=%+v err=%v", byModel, err)
		}
		all, err := queries.GetTokenUsageByDayAndType(ctx, sqlc.GetTokenUsageByDayAndTypeParams{BotID: botID, FromTime: from, ToTime: to})
		if err != nil || len(all) != 1 || all[0].InputTokens != 1000 || all[0].CacheReadTokens != 800 || !all[0].CacheReadTokensReported {
			t.Fatalf("all usage=%+v err=%v", all, err)
		}
		filtered, err := queries.GetTokenUsageByDayAndType(ctx, sqlc.GetTokenUsageByDayAndTypeParams{BotID: botID, FromTime: from, ToTime: to, ModelID: replacementUUID})
		if err != nil || len(filtered) != 0 {
			t.Fatalf("replacement cannot claim old usage: %+v err=%v", filtered, err)
		}
		records, err := queries.ListTokenUsageRecords(ctx, sqlc.ListTokenUsageRecordsParams{BotID: botID, FromTime: from, ToTime: to, PageLimit: 10})
		if err != nil || len(records) != 1 || records[0].ModelID.Valid || records[0].InputTokens != 1000 || records[0].CacheReadTokens != 800 {
			t.Fatalf("records=%+v err=%v", records, err)
		}
		var persisted json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT usage FROM bot_history_messages WHERE id=$1`, saved.ID).Scan(&persisted); err != nil {
			t.Fatal(err)
		}
		var usage sdk.Usage
		if err := json.Unmarshal(persisted, &usage); err != nil {
			t.Fatal(err)
		}
		if usage.InputTokens != 1000 || usage.OutputTokens != 200 || usage.InputTokenDetails.CacheReadTokens != 800 {
			t.Fatalf("stored usage changed: %s", persisted)
		}
	}
}
