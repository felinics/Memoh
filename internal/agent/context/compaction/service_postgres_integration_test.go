package compaction

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/checkpoint"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

// A pass over a long run of short messages, whose stored rows weigh far more
// than their payload, commits every row its candidate read admitted but the
// newest the verbatim budget keeps: the checkpoint it makes active holds its
// summary alone, through the last row it folds.
func TestPostgresCompactionCommitsEveryAdmittedRow(t *testing.T) {
	ctx := context.Background()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN to run the compaction PostgreSQL integration")
	}
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		if err := dbtest.MigratePostgresUp(dsn); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	userID, botID, sessionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, fixture := range []struct {
		sql  string
		args []any
	}{
		{`WITH u AS (INSERT INTO users (id, username, is_active) VALUES ($1::uuid, $1::text, true) RETURNING id)
			INSERT INTO team_members (user_id, role) SELECT id, 'admin' FROM u`, []any{userID}},
		{`INSERT INTO bots (id, owner_user_id, name) VALUES ($1::uuid, $2, $1::text)`, []any{botID, userID}},
		{`INSERT INTO bot_sessions (id, bot_id, channel_type) VALUES ($1, $2, 'local')`, []any{sessionID, botID}},
	} {
		if _, err := pool.Exec(ctx, fixture.sql, fixture.args...); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bots WHERE id=$1`, botID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	})

	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	messages := messagepkg.NewService(slog.Default(), queries)
	const rows = 1200
	for i := range rows {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		content, err := historyfrag.MarshalStoredModelMessage(turn.ModelMessage{Role: role, Content: turn.NewTextContent(fmt.Sprintf("short message %04d", i))})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := messages.Persist(ctx, messagepkg.PersistInput{BotID: botID, SessionID: sessionID, Role: role, Content: content}); err != nil {
			t.Fatalf("persist row %d: %v", i, err)
		}
	}

	summarizer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, `data: {"id":"summary","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"The short messages were exchanged."},"finish_reason":null}]}`+"\n\n"+
			`data: {"id":"summary","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(summarizer.Close)

	cfg := TriggerConfig{
		BotID: botID, SessionID: sessionID, ModelID: "compact-model", ClientType: "openai-completions",
		BaseURL: summarizer.URL, APIKey: "test-key", SummaryWindowTokens: 60000, HistoryBudgetTokens: 4000,
	}
	passes, err := NewService(slog.Default(), queries).Compact(ctx, cfg)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	active, err := checkpoint.NewStore(slog.Default(), queries).Active(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var folded, total int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM bot_history_messages WHERE session_id = $1 AND (turn_position, turn_message_seq) <= ($2, $3)),
		  (SELECT count(*) FROM bot_history_messages WHERE session_id = $1)`,
		sessionID, active.Position.Turn, active.Position.Seq).Scan(&folded, &total); err != nil {
		t.Fatal(err)
	}
	if len(passes) != 1 {
		t.Fatalf("passes=%d, want one committed pass", len(passes))
	}
	// The newest rows the verbatim budget holds stay raw, each taking at
	// least a token of it.
	budget := verbatimBudget(Event{Allowance: cfg.HistoryBudgetTokens + forkTokens(sdk.Request{System: synthesisSystemPrompt})})
	if kept := total - folded; total != rows || kept < 1 || kept > budget {
		t.Fatalf("folded=%d of %d, want every row folded but the newest the %d-token budget keeps", folded, total, budget)
	}
	if len(active.Items) != 1 || active.Items[0].SummaryID != passes[0].SummaryID || active.Items[0].Summary != "The short messages were exchanged." {
		t.Fatalf("checkpoint holds %+v, want the pass's summary alone", active.Items)
	}
}
