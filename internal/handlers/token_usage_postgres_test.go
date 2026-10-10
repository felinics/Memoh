package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/bots"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	adapters "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/memory/memllm"
	"github.com/felinics/memoh/internal/models"
)

const (
	memoryUsageReported   = `{"id":"msg_memory","type":"message","role":"assistant","content":[{"type":"text","text":"[]"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"cache_read_input_tokens":200,"cache_creation_input_tokens":100,"output_tokens":5}}`
	memoryUsageUnreported = `{"id":"resp_memory","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"[]"}]}],"usage":{"input_tokens":500,"output_tokens":5,"total_tokens":505}}`
	memoryUsageLegacy     = `{"inputTokens":310,"outputTokens":5,"inputTokenDetails":{"cacheReadTokens":200}}`
)

// Memory LLM calls are written the way the server records them: the SDK usage
// of each provider response, marshalled into bot_memory_usage. Rows written
// before cache reporting existed carry no reporting field.
func TestPostgresTokenUsageReportsMemoryCacheReads(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		if os.Getenv("MEMOH_TEST_POSTGRES_REQUIRED") == "1" {
			t.Fatal("TEST_POSTGRES_DSN is not set")
		}
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	for _, tt := range []struct {
		name            string
		rows            []string
		input, cache    int64
		reported        bool
		reportedRecords int
	}{
		{name: "reported", rows: []string{memoryUsageReported}, input: 310, cache: 200, reported: true, reportedRecords: 1},
		{name: "reported and unreported", rows: []string{memoryUsageReported, memoryUsageUnreported}, input: 810, cache: 200, reportedRecords: 1},
		{name: "legacy", rows: []string{memoryUsageLegacy}, input: 310, cache: 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			userID, botID := uuid.NewString(), uuid.NewString()
			if _, err := tx.Exec(ctx, `WITH u AS (INSERT INTO users(id,username,is_active) VALUES($1,$2,true) RETURNING id) INSERT INTO team_members(user_id,role) SELECT id,'admin' FROM u`, userID, "memory-"+userID); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO bots(id,owner_user_id,name) VALUES($1,$2,$3)`, botID, userID, "memory-"+botID); err != nil {
				t.Fatal(err)
			}
			queries := sqlc.New(tx)
			botUUID, _ := dbpkg.ParseUUID(botID)
			for _, row := range tt.rows {
				recordMemoryUsage(t, ctx, queries, botUUID, row)
			}

			store := postgresstore.NewQueries(queries)
			handler := NewTokenUsageHandler(slog.Default(), store, bots.NewService(nil, store), newTestAdminAccountService("admin"))
			from, to := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly), time.Now().UTC().AddDate(0, 0, 2).Format(time.DateOnly)
			for _, filter := range []string{"", "&session_type=memory"} {
				var usage TokenUsageResponse
				serveTokenUsage(t, botID, "/bots/:bot_id/token-usage", fmt.Sprintf("/bots/%s/token-usage?from=%s&to=%s%s", botID, from, to, filter), handler.GetTokenUsage, &usage)
				if len(usage.Memory) != 1 || usage.Memory[0].InputTokens != tt.input || usage.Memory[0].CacheReadTokens != tt.cache || usage.Memory[0].CacheReadTokensReported != tt.reported {
					t.Fatalf("filter %q memory usage = %+v, want input %d cache %d reported %t", filter, usage.Memory, tt.input, tt.cache, tt.reported)
				}
			}
			var records TokenUsageRecordsResponse
			serveTokenUsage(t, botID, "/bots/:bot_id/token-usage/records", fmt.Sprintf("/bots/%s/token-usage/records?from=%s&to=%s&session_type=memory", botID, from, to), handler.ListTokenUsageRecords, &records)
			reported := 0
			for _, record := range records.Items {
				if record.CacheReadTokensReported {
					reported++
				}
			}
			if len(records.Items) != len(tt.rows) || reported != tt.reportedRecords {
				t.Fatalf("memory records = %+v, want %d reported", records.Items, tt.reportedRecords)
			}
		})
	}
}

func recordMemoryUsage(t *testing.T, ctx context.Context, queries *sqlc.Queries, botID pgtype.UUID, row string) {
	t.Helper()
	if row == memoryUsageLegacy {
		if err := queries.CreateMemoryUsage(ctx, sqlc.CreateMemoryUsageParams{BotID: botID, Operation: memllm.OperationExtract, Usage: []byte(row)}); err != nil {
			t.Fatal(err)
		}
		return
	}
	clientType := models.ClientTypeAnthropicMessages
	if row == memoryUsageUnreported {
		clientType = models.ClientTypeOpenAIResponses
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, row)
	}))
	defer server.Close()
	var recordErr error
	client := memllm.New(memllm.Config{ModelID: "memory-test", BaseURL: server.URL, APIKey: "fixture", ClientType: string(clientType), OnUsage: func(ctx context.Context, operation string, usage sdk.Usage) {
		payload, err := json.Marshal(usage)
		if err == nil {
			err = queries.CreateMemoryUsage(ctx, sqlc.CreateMemoryUsageParams{BotID: botID, Operation: operation, Usage: payload})
		}
		recordErr = err
	}})
	if _, err := client.Extract(ctx, adapters.ExtractRequest{Messages: []adapters.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if recordErr != nil {
		t.Fatal(recordErr)
	}
}

func serveTokenUsage(t *testing.T, botID, route, target string, serve echo.HandlerFunc, out any) {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, httptest.NewRequest(http.MethodGet, target, nil), rec, uuid.NewString())
	ctx.SetPath(route)
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)
	if err := serve(ctx); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%s: err=%v status=%d body=%s", target, err, rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatal(err)
	}
}
