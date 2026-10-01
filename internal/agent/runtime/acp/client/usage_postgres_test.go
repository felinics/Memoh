package client

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/felinics/twilight/sdk"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/messageconv"
)

func TestPostgresACPUsageReporting(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		if os.Getenv("MEMOH_TEST_POSTGRES_REQUIRED") == "1" {
			t.Fatal("TEST_POSTGRES_DSN is not set")
		}
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	for _, tt := range []struct {
		name     string
		reads    []*int
		beside   bool
		reported bool
		cache    int64
	}{
		{name: "absent", reads: []*int{nil}},
		{name: "zero", reads: []*int{acp.Ptr(0)}, reported: true},
		{name: "positive", reads: []*int{acp.Ptr(3)}, reported: true, cache: 3},
		{name: "known mixed", reads: []*int{acp.Ptr(0), acp.Ptr(3)}, reported: true, cache: 3},
		{name: "unknown mixed", reads: []*int{acp.Ptr(3), nil}, cache: 3},
		{name: "cache beside input", reads: []*int{acp.Ptr(200)}, beside: true, reported: true, cache: 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
			userID, botID, sessionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
			if _, err := tx.Exec(ctx, `WITH u AS (INSERT INTO users(id,username,is_active) VALUES($1,$2,true) RETURNING id) INSERT INTO team_members(user_id,role) SELECT id,'admin' FROM u`, userID, "acp-"+userID); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO bots(id,owner_user_id,name) VALUES($1,$2,'acp-usage')`, botID, userID); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO bot_sessions(id,bot_id,channel_type) VALUES($1,$2,'local')`, sessionID, botID); err != nil {
				t.Fatal(err)
			}
			queries := sqlc.New(tx)
			store := postgresstore.NewQueries(queries)
			botUUID, _ := dbpkg.ParseUUID(botID)
			sessionUUID, _ := dbpkg.ParseUUID(sessionID)
			wantInput := int64(0)
			for _, read := range tt.reads {
				raw := acp.Usage{InputTokens: 10, OutputTokens: 7, TotalTokens: 17, CachedReadTokens: read}
				wantInput += 10
				if tt.beside {
					raw.TotalTokens += *read
					wantInput += int64(*read)
				}
				usage := promptUsageFromACP(&raw)
				output := attachUsageToLastAssistant([]sdk.Message{{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: "ok"}}}}, usage)
				converted := messageconv.SDKMessagesToModelMessages(output)[0]
				saved, err := store.CreateMessage(ctx, sqlc.CreateMessageParams{BotID: botUUID, SessionID: sessionUUID, Role: converted.Role, Content: converted.Content, Metadata: []byte(`{}`), Usage: converted.Usage, SessionMode: "chat", RuntimeType: "acp_agent"})
				if err != nil {
					t.Fatal(err)
				}
				var got sdk.Usage
				if err := json.Unmarshal(saved.Usage, &got); err != nil {
					t.Fatal(err)
				}
				if got.CacheReadTokensReported != (read != nil) {
					t.Fatalf("persisted cache reporting lost: %s", saved.Usage)
				}
			}
			from := pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}
			to := pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}
			for range 2 {
				rows, err := queries.GetTokenUsageByDayAndType(ctx, sqlc.GetTokenUsageByDayAndTypeParams{BotID: botUUID, FromTime: from, ToTime: to})
				if err != nil || len(rows) != 1 || rows[0].SessionType != "acp_agent" || rows[0].CacheReadTokensReported != tt.reported || rows[0].CacheReadTokens != tt.cache || rows[0].InputTokens != wantInput {
					t.Fatalf("daily usage=%+v err=%v want reported=%t cache=%d", rows, err, tt.reported, tt.cache)
				}
			}
		})
	}
}
