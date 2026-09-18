package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

func historySearchPostgres(t *testing.T) (*HistoryProvider, *messagepkg.DBService, pgx.Tx) {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	userID := "00000000-0000-0000-0000-000000098100"
	if _, err := tx.Exec(ctx, `WITH inserted AS (INSERT INTO users (id, username, is_active) VALUES ($1, 'history-search-fixture', true) RETURNING id) INSERT INTO team_members (user_id, role) SELECT id, 'admin' FROM inserted`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bots (id, owner_user_id, name) VALUES ($1, $2, 'history-search-fixture')`, searchTestBotID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bot_sessions (id, bot_id, channel_type) VALUES ($1, $2, 'local')`, searchTestSessionID, searchTestBotID); err != nil {
		t.Fatal(err)
	}
	queries := postgresstore.NewQueries(sqlc.New(tx))
	service := messagepkg.NewService(nil, queries)
	return NewHistoryProvider(nil, nil, service, queries), service, tx
}

func persistSearchFixture(t *testing.T, service *messagepkg.DBService, role, content string) messagepkg.Message {
	t.Helper()
	message, err := service.Persist(context.Background(), messagepkg.PersistInput{BotID: searchTestBotID, SessionID: searchTestSessionID, Role: role, Content: json.RawMessage(content)})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func TestHistorySearchPostgresExecutionEvidenceAndVisibility(t *testing.T) {
	provider, service, tx := historySearchPostgres(t)
	old := persistSearchFixture(t, service, "user", `{"role":"user","content":"old_unique"}`)
	exact, err := service.GetByIDBySession(context.Background(), searchTestSessionID, old.ID)
	if err != nil || exact.TurnID == "" || exact.TurnID != old.TurnID {
		t.Fatalf("exact turn identity: %+v %v", exact, err)
	}
	if _, err := tx.Exec(context.Background(), `UPDATE bot_history_messages SET created_at = now() - interval '30 days' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	persistSearchFixture(t, service, "assistant", `{"role":"assistant","content":[{"type":"reasoning","text":"private_reasoning"},{"type":"tool-call","toolCallId":"native_call","toolName":"native_lookup","input":{"code":"native_input"},"providerMetadata":{"private":"private_provider"}}]}`)
	persistSearchFixture(t, service, "assistant", `{"role":"assistant","content":[{"type":"tool-call","toolCallId":"args_call","toolName":"args_lookup","args":{"code":"legacy_args_input"}}]}`)
	persistSearchFixture(t, service, "tool", `{"role":"tool","content":[{"type":"tool-result","toolCallId":"native_call","toolName":"native_lookup","result":{"value":"native_result"},"isError":true}]}`)
	persistSearchFixture(t, service, "assistant", `{"role":"assistant","content":"","tool_calls":[{"id":"legacy_call","type":"function","function":{"name":"legacy_lookup","arguments":"{\"code\":\"legacy_input\"}"}}]}`)
	persistSearchFixture(t, service, "tool", `{"role":"tool","content":[{"type":"tool-result","toolCallId":"legacy_call","output":{"type":"error-text","value":"legacy_result"}}]}`)
	persistSearchFixture(t, service, "tool", `{"role":"tool","content":{"value":"legacy_object_result"},"tool_call_id":"legacy_object_call","name":"legacy_object_name"}`)
	persistSearchFixture(t, service, "tool", `{"role":"tool","content":123456789,"tool_call_id":"legacy_scalar_call"}`)
	persistSearchFixture(t, service, "tool", `{"type":"tool-result","toolCallId":"bare_call","result":"bare_result"}`)
	persistSearchFixture(t, service, "tool", `{"role":"tool","content":[{"status":"legacy_array_result"},42],"tool_call_id":"legacy_array_call"}`)
	persistSearchFixture(t, service, "tool", `{"role":"tool","tool_call_id":"hybrid_call","content":[{"type":"tool-result","toolCallId":"hybrid_call","result":"hybrid_result","providerMetadata":{"private":"hybrid_private"}},{"type":"reasoning","text":"hybrid_private"},{},42]}`)
	persistSearchFixture(t, service, "tool", `{"role":"tool","tool_call_id":"opaque_call","content":[{"type":"reasoning","text":"opaque_reasoning_data"}]}`)

	persistSearchFixture(t, service, "user", `[{"type":"text","text":"array_literal_100%"}]`)
	for _, keyword := range []string{"old_unique", "native_lookup", "native_input", "legacy_args_input", "native_result", "legacy_lookup", "legacy_input", "legacy_result", "legacy_object_result", "legacy_object_call", "legacy_object_name", "123456789", "bare_result", "legacy_array_result", "hybrid_result", "opaque_reasoning_data", "array_literal_100%"} {
		out, err := executeHistorySearch(t, provider, map[string]any{"session_id": searchTestSessionID, "keyword": keyword})
		if err != nil {
			t.Fatalf("%s: %v", keyword, err)
		}
		if out.(map[string]any)["count"].(int) == 0 {
			t.Fatalf("missing %q in %v", keyword, out)
		}
	}
	for _, keyword := range []string{"private_reasoning", "private_provider", "hybrid_private", "array_literal_100_"} {
		out, err := executeHistorySearch(t, provider, map[string]any{"session_id": searchTestSessionID, "keyword": keyword})
		if err != nil {
			t.Fatal(err)
		}
		if out.(map[string]any)["count"] != 0 {
			t.Fatalf("unexpected private or wildcard match %q: %v", keyword, out)
		}
	}
	out, err := executeHistorySearch(t, provider, map[string]any{"keyword": "old_unique"})
	if err != nil || out.(map[string]any)["count"] != 0 {
		t.Fatalf("recent scope: %v %v", out, err)
	}
	if _, err := tx.Exec(context.Background(), `UPDATE bot_history_messages SET turn_visible = false WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	out, err = executeHistorySearch(t, provider, map[string]any{"session_id": searchTestSessionID, "keyword": "old_unique"})
	if err != nil || out.(map[string]any)["count"] != 0 {
		t.Fatalf("hidden row: %v %v", out, err)
	}
}

func TestHistorySearchPostgresCursorSurvivesDeletedAnchor(t *testing.T) {
	provider, service, tx := historySearchPostgres(t)
	for range 3 {
		persistSearchFixture(t, service, "user", `{"role":"user","content":"tied"}`)
	}
	if _, err := tx.Exec(context.Background(), `UPDATE bot_history_messages SET created_at = '2026-01-01T00:00:00Z' WHERE session_id = $1`, searchTestSessionID); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"session_id": searchTestSessionID, "limit": 1}
	seen := make(map[string]bool)
	for page := 0; page < 3; page++ {
		value, err := executeHistorySearch(t, provider, args)
		if err != nil {
			t.Fatal(err)
		}
		out := value.(map[string]any)
		rows := out["messages"].([]map[string]any)
		if len(rows) != 1 {
			t.Fatalf("page %d: %v", page, out)
		}
		id := rows[0]["id"].(string)
		if seen[id] {
			t.Fatalf("duplicate row %s", id)
		}
		seen[id] = true
		if page == 2 {
			if out["has_more"] != false {
				t.Fatalf("last page: %v", out)
			}
		} else {
			if out["has_more"] != true {
				t.Fatalf("missing continuation: %v", out)
			}
			args["cursor"] = out["next_cursor"]
			if err := service.DeleteByIDs(context.Background(), []string{id}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestHistorySearchPostgresDeletedSessionCannotBeRead(t *testing.T) {
	provider, service, tx := historySearchPostgres(t)
	message := persistSearchFixture(t, service, "user", `{"role":"user","content":"deleted_scope"}`)
	if _, err := tx.Exec(context.Background(), `UPDATE bot_sessions SET deleted_at = now() WHERE id = $1`, searchTestSessionID); err != nil {
		t.Fatal(err)
	}
	out, err := executeHistorySearch(t, provider, map[string]any{"session_id": searchTestSessionID})
	if err != nil || out.(map[string]any)["count"] != 0 {
		t.Fatalf("deleted search: %v %v", out, err)
	}
	if _, err := service.GetByIDBySession(context.Background(), searchTestSessionID, message.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleted exact read: %v", err)
	}
	if rows, err := service.ListLatestBySession(context.Background(), searchTestSessionID, 10); err != nil || len(rows) != 0 {
		t.Fatalf("deleted latest: %v %v", rows, err)
	}
	if rows, err := service.ListBeforeBySession(context.Background(), searchTestSessionID, time.Now().Add(time.Hour), 10); err != nil || len(rows) != 0 {
		t.Fatalf("deleted before: %v %v", rows, err)
	}
	rows, err := sqlc.New(tx).ListMessagesBeforeCursorBySession(context.Background(), sqlc.ListMessagesBeforeCursorBySessionParams{SessionID: dbpkg.ParseUUIDOrEmpty(searchTestSessionID), CursorTurnPosition: 999, CursorTurnMessageSeq: 999, CursorCreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}, CursorMessageID: dbpkg.ParseUUIDOrEmpty(message.ID), MaxCount: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("deleted cursor: %v %v", rows, err)
	}
}

func TestHistorySearchPostgresPreviewFindsLateMatch(t *testing.T) {
	provider, service, _ := historySearchPostgres(t)
	content, _ := json.Marshal(map[string]any{"role": "user", "content": strings.Repeat("🧭", 10000) + "late_match" + strings.Repeat("后", 1000)})
	persistSearchFixture(t, service, "user", string(content))
	value, err := executeHistorySearch(t, provider, map[string]any{"session_id": searchTestSessionID, "keyword": "late_match"})
	if err != nil {
		t.Fatal(err)
	}
	rows := value.(map[string]any)["messages"].([]map[string]any)
	if len(rows) != 1 || !strings.Contains(rows[0]["text"].(string), "late_match") || len(rows[0]["text"].(string)) > 512 {
		t.Fatalf("bad preview: %v", rows)
	}
}

func TestHistorySearchPostgresMalformedLegacyIDsExcludeReasoning(t *testing.T) {
	provider, service, _ := historySearchPostgres(t)
	persistSearchFixture(t, service, "user", `{"role":"user","content":"inspect"}`)
	for _, rawID := range []string{`null`, `""`, `17`, `{}`, `" \t\n\r\f\u000b"`} {
		t.Run(rawID, func(t *testing.T) {
			msg := persistSearchFixture(t, service, "tool", `{"role":"tool","tool_call_id":`+rawID+`,"content":[{"type":"reasoning","text":"malformed_private_reasoning"}]}`)
			out, err := executeHistoryRead(t, provider, map[string]any{"message_id": msg.ID, "view": "execution"})
			if err == nil {
				encoded, _ := json.Marshal(out)
				if strings.Contains(string(encoded), "malformed_private_reasoning") {
					t.Error("invalid legacy ID exposed reasoning in the execution view")
				}
			}
		})
	}
	out, err := executeHistorySearch(t, provider, map[string]any{"session_id": searchTestSessionID, "keyword": "malformed_private_reasoning"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["count"] != 0 {
		t.Fatalf("invalid legacy IDs exposed reasoning in search: %v", out)
	}
	for _, id := range []string{"opaque-call", "\u00a0"} {
		content, _ := json.Marshal(map[string]any{
			"role": "tool", "tool_call_id": id,
			"content": []map[string]any{{"type": "reasoning", "text": "opaque_result_data"}},
		})
		msg := persistSearchFixture(t, service, "tool", string(content))
		out, err := executeHistoryRead(t, provider, map[string]any{"message_id": msg.ID, "view": "execution"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out["messages"].([]map[string]any)[0]["content"].(string), "opaque_result_data") {
			t.Fatal("opaque legacy tool result data was lost")
		}
	}
	out, err = executeHistorySearch(t, provider, map[string]any{"session_id": searchTestSessionID, "keyword": "opaque_result_data"})
	if err != nil || out.(map[string]any)["count"] != 2 {
		t.Fatalf("opaque legacy tool results were lost in search: %v %v", out, err)
	}
}
