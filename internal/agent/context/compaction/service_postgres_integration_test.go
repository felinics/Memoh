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

	"github.com/google/uuid"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

// A pass that claims every row its candidate read admitted commits: the
// rows read back at commit are measured as the candidate read measured them,
// so a long run of short messages, whose stored rows weigh far more than
// their payload, is not mistaken for rows that lost their claim.
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
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"summary","object":"chat.completion","model":"compact-model","choices":[{"index":0,"message":{"role":"assistant","content":"The short messages were exchanged."},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(summarizer.Close)

	passes, err := NewService(slog.Default(), queries).Compact(ctx, TriggerConfig{
		BotID: botID, SessionID: sessionID, ModelID: "compact-model", ClientType: "openai-completions",
		BaseURL: summarizer.URL, APIKey: "test-key", SummaryWindowTokens: 60000, HistoryBudgetTokens: 4000,
	})
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	var folded int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_history_messages h JOIN bot_history_message_compacts c ON c.id=h.compact_id AND c.status='ok' WHERE h.session_id=$1`, sessionID).Scan(&folded); err != nil {
		t.Fatal(err)
	}
	if len(passes) != 1 || folded != rows {
		t.Fatalf("passes=%d folded=%d, want every row folded by one committed pass", len(passes), folded)
	}
}
