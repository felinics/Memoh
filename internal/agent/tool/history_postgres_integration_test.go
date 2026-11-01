package tools

import (
	"context"
	"os"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/toolexec"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

// A ref reads back, page by page, exactly the text the provider received for
// a tool result persisted through the history write path, and a row of
// another session of the same bot stays unreadable by ref.
func TestPostgresHistoryToolResultRef(t *testing.T) {
	ctx := context.Background()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN to run the history ref PostgreSQL integration")
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
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	userID, botID, currentID, otherID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, fixture := range []struct {
		sql  string
		args []any
	}{
		{`WITH u AS (INSERT INTO users (id, username, is_active) VALUES ($1::uuid, $1::text, true) RETURNING id)
			INSERT INTO team_members (user_id, role) SELECT id, 'admin' FROM u`, []any{userID}},
		{`INSERT INTO bots (id, owner_user_id, name) VALUES ($1::uuid, $2, $1::text)`, []any{botID, userID}},
		{`INSERT INTO bot_sessions (id, bot_id, channel_type) VALUES ($1, $3, 'local'), ($2, $3, 'local')`, []any{currentID, otherID, botID}},
	} {
		if _, err := tx.Exec(ctx, fixture.sql, fixture.args...); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}

	messages := messagepkg.NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))
	output, err := sdk.JSONOutput(map[string]any{"exit_code": 0, "stdout": strings.Repeat("line \"<quoted>\" & ünï 你好\n", 400)})
	if err != nil {
		t.Fatalf("encode output: %v", err)
	}
	content, err := historyfrag.MarshalStoredSDKMessage(sdk.Message{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{
		sdk.ToolResultPart{ToolCallID: "call-1", ToolName: "exec", Result: output},
	}})
	if err != nil {
		t.Fatalf("marshal tool row: %v", err)
	}
	rows := map[string]string{}
	for _, sessionID := range []string{currentID, otherID} {
		_, err := messages.Persist(ctx, messagepkg.PersistInput{BotID: botID, SessionID: sessionID, Role: "user", Content: []byte(`{"role":"user","content":"run it"}`)})
		row, toolErr := messages.Persist(ctx, messagepkg.PersistInput{BotID: botID, SessionID: sessionID, Role: "tool", Content: content})
		if err != nil || toolErr != nil {
			t.Fatalf("persist turn: %v, %v", err, toolErr)
		}
		rows[sessionID] = row.ID
	}

	limit := ToolOutputLimit{MaxBytes: 4096, MaxLines: 64}
	tools, err := NewHistoryProvider(nil, nil, messages, nil, limit).Tools(ctx, SessionContext{BotID: botID, SessionID: currentID})
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	getMessages := toolByNameForTest(t, WrapToolOutputLimits(tools, limit), ToolGetMessages())
	if pages := readToolResultRef(t, getMessages, rows[currentID]+":0", output.String()); pages < 2 {
		t.Fatalf("pages = %d, want the result split across pages", pages)
	}
	out, err := getMessages.Execute(&toolexec.ToolExecContext{Context: ctx, ToolName: getMessages.Name},
		toolexec.ArgumentsFromValue(map[string]any{"ref": rows[otherID] + ":0"}))
	if err == nil || !strings.Contains(err.Error(), "not in the current session") {
		t.Fatalf("ref into another session = %s, %v; want a current-session refusal", out.String(), err)
	}
}
