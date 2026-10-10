package client

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/messageconv"
)

// Each case is reported by a fake ACP agent over JSON-RPC, so the wire shape
// claude-agent-acp and codex-acp send reaches the usage queries unchanged.
func TestPostgresACPUsageReporting(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		if os.Getenv("MEMOH_TEST_POSTGRES_REQUIRED") == "1" {
			t.Fatal("TEST_POSTGRES_DSN is not set")
		}
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	for _, tt := range []struct {
		name            string
		usages          []acp.Usage
		input, cache    int64
		reported        bool
		reportedRecords int
	}{
		{name: "absent", usages: []acp.Usage{{InputTokens: 10, OutputTokens: 7, TotalTokens: 17}}, input: 10},
		{name: "zero", usages: []acp.Usage{{InputTokens: 10, OutputTokens: 7, TotalTokens: 17, CachedReadTokens: acp.Ptr(0)}}, input: 10, reported: true, reportedRecords: 1},
		{name: "known mixed", usages: []acp.Usage{{InputTokens: 10, OutputTokens: 7, TotalTokens: 17, CachedReadTokens: acp.Ptr(0)}, {InputTokens: 10, OutputTokens: 7, TotalTokens: 17, CachedReadTokens: acp.Ptr(3)}}, input: 20, cache: 3, reported: true, reportedRecords: 2},
		{name: "unknown mixed", usages: []acp.Usage{{InputTokens: 10, OutputTokens: 7, TotalTokens: 17, CachedReadTokens: acp.Ptr(3)}, {InputTokens: 10, OutputTokens: 7, TotalTokens: 17}}, input: 20, cache: 3, reportedRecords: 1},
		{name: "claude-agent-acp cache beside input", usages: []acp.Usage{{InputTokens: 10, OutputTokens: 7, TotalTokens: 317, CachedReadTokens: acp.Ptr(200), CachedWriteTokens: acp.Ptr(100)}}, input: 310, cache: 200, reported: true, reportedRecords: 1},
		{name: "codex-acp cached read", usages: []acp.Usage{{InputTokens: 1500, OutputTokens: 450, TotalTokens: 2450, CachedReadTokens: acp.Ptr(500)}}, input: 2000, cache: 500, reported: true, reportedRecords: 1},
		{name: "opencode thought in total", usages: []acp.Usage{{InputTokens: 1000, OutputTokens: 7, TotalTokens: 1240, CachedReadTokens: acp.Ptr(200), ThoughtTokens: acp.Ptr(33)}}, input: 1200, cache: 200, reported: true, reportedRecords: 1},
		{name: "cache larger than input", usages: []acp.Usage{{InputTokens: 10, OutputTokens: 7, TotalTokens: 17, CachedReadTokens: acp.Ptr(200)}}, input: 210, cache: 200, reported: true, reportedRecords: 1},
		{name: "cache larger than input, total fits neither", usages: []acp.Usage{{InputTokens: 10, OutputTokens: 7, TotalTokens: 50, CachedReadTokens: acp.Ptr(200)}}, input: 210, cache: 200, reported: true, reportedRecords: 1},
		{name: "total fits neither accounting", usages: []acp.Usage{{InputTokens: 310, OutputTokens: 7, TotalTokens: 400, CachedReadTokens: acp.Ptr(200)}}, input: 310, cache: 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
			runner, agentPath := newStartSessionTestRunner(t)
			for _, usage := range tt.usages {
				reported, err := json.Marshal(usage)
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("MEMOH_ACP_FAKE_AGENT_USAGE", string(reported))
				sess, err := runner.StartSession(ctx, StartRequest{AgentID: acpprofile.AgentACPID, BotID: botID, ProjectPath: "/data/project", Command: agentPath, Timeout: 10 * time.Second}, nil)
				if err != nil {
					t.Fatal(err)
				}
				result, err := sess.Prompt(ctx, "hi")
				_ = sess.Close()
				if err != nil {
					t.Fatal(err)
				}
				for _, msg := range messageconv.SDKMessagesToModelMessages(result.Output) {
					if _, err := store.CreateMessage(ctx, sqlc.CreateMessageParams{BotID: botUUID, SessionID: sessionUUID, Role: msg.Role, Content: msg.Content, Metadata: []byte(`{}`), Usage: msg.Usage, SessionMode: "chat", RuntimeType: "acp_agent"}); err != nil {
						t.Fatal(err)
					}
				}
			}
			from := pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}
			to := pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}
			days, err := queries.GetTokenUsageByDayAndType(ctx, sqlc.GetTokenUsageByDayAndTypeParams{BotID: botUUID, FromTime: from, ToTime: to})
			if err != nil || len(days) != 1 || days[0].SessionType != "acp_agent" || days[0].InputTokens != tt.input || days[0].CacheReadTokens != tt.cache || days[0].CacheReadTokensReported != tt.reported {
				t.Fatalf("daily usage=%+v err=%v want input=%d cache=%d reported=%t", days, err, tt.input, tt.cache, tt.reported)
			}
			records, err := queries.ListTokenUsageRecords(ctx, sqlc.ListTokenUsageRecordsParams{BotID: botUUID, FromTime: from, ToTime: to, PageLimit: 10})
			if err != nil || len(records) != len(tt.usages) {
				t.Fatalf("records=%+v err=%v", records, err)
			}
			reportedRecords := 0
			for _, record := range records {
				if record.CacheReadTokensReported {
					reportedRecords++
				}
			}
			if reportedRecords != tt.reportedRecords {
				t.Fatalf("records=%+v want %d reported", records, tt.reportedRecords)
			}
		})
	}
}
