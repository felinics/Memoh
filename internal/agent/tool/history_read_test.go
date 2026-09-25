package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

func executeHistoryRead(t *testing.T, provider *HistoryProvider, args map[string]any) (map[string]any, error) {
	t.Helper()
	available, err := provider.Tools(context.Background(), SessionContext{BotID: searchTestBotID, SessionID: searchTestSessionID})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range available {
		if tool.Name == ToolGetMessages().String() {
			got, err := tool.Execute(&sdk.ToolExecContext{Context: context.Background()}, args)
			if err != nil {
				return nil, err
			}
			return got.(map[string]any), nil
		}
	}
	t.Fatal("get_messages missing")
	return nil, nil
}

func TestHistoryReadRejectsFractionalLimits(t *testing.T) {
	t.Parallel()
	provider := NewHistoryProvider(nil, nil, &fakeHistoryMessageReader{}, nil)
	for _, limit := range []float64{1.5, -0.5} {
		if _, err := executeHistoryRead(t, provider, map[string]any{"limit": limit}); err == nil {
			t.Errorf("accepted fractional limit %v", limit)
		}
	}
	if _, err := executeHistoryRead(t, provider, map[string]any{"limit": float64(1)}); err != nil {
		t.Fatalf("rejected integer JSON number: %v", err)
	}
}

func TestHistoryReadPostgresMessageCursorPreservesTurnOrder(t *testing.T) {
	provider, service, tx := historySearchPostgres(t)
	var want []string
	for i := 0; i < 7; i++ {
		msg := persistSearchFixture(t, service, "user", fmt.Sprintf(`{"role":"user","content":"message %d"}`, i))
		want = append(want, msg.ID)
	}
	if _, err := tx.Exec(context.Background(), `UPDATE bot_history_messages SET created_at='2026-01-01T00:00:00Z' WHERE bot_id=$1`, searchTestBotID); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"limit": 2}
	var ids []string
	for i := 0; i < 5; i++ {
		out, err := executeHistoryRead(t, provider, args)
		if err != nil {
			t.Fatal(err)
		}
		rows := out["messages"].([]map[string]any)
		if len(rows) > 2 {
			t.Fatal("page exceeded requested count")
		}
		var page []string
		for _, row := range rows {
			id := row["id"].(string)
			if slices.Contains(ids, id) {
				t.Fatalf("message cursor repeated %s", id)
			}
			page = append(page, id)
		}
		ids = append(page, ids...)
		if out["has_more"] == false {
			break
		}
		cursor, ok := out["next_before_message_id"].(string)
		if !ok || cursor == "" {
			t.Fatalf("page did not expose a continuation: %v", out)
		}
		args["before_message_id"] = cursor
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("read ids %v, want %v", ids, want)
	}
}

func TestHistoryReadPageBudgetKeepsContinuation(t *testing.T) {
	t.Parallel()
	reader := &fakeHistoryMessageReader{}
	for i := 99; i >= 0; i-- {
		reader.latestMessages = append(reader.latestMessages, historyTestMessage(t, fmt.Sprintf("message-%d", i), searchTestSessionID, "user", strings.Repeat("long evidence 错误", 1000), time.Time{}))
	}
	provider := NewHistoryProvider(nil, nil, reader, nil)
	out, err := executeHistoryRead(t, provider, map[string]any{"limit": 100})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(out)
	if len(encoded) > 32*1024 || out["has_more"] != true {
		t.Fatalf("unbounded/incomplete page: bytes=%d, has_more=%v", len(encoded), out["has_more"])
	}
	rows := out["messages"].([]map[string]any)
	if len(rows) == 0 || rows[len(rows)-1]["id"] != "message-99" || out["next_before_message_id"] != rows[0]["id"] {
		t.Fatalf("page must keep newest rows with a resumable older boundary: %v", out)
	}
	if rows[0]["text_truncated"] != true {
		t.Fatal("clipped chat text must be marked for exact execution read")
	}
}

func TestHistoryReadRejectsAmbiguousMessageCursors(t *testing.T) {
	t.Parallel()
	provider := NewHistoryProvider(nil, nil, &fakeHistoryMessageReader{}, nil)
	for _, args := range []map[string]any{
		{"message_id": "exact", "before_message_id": "older"},
		{"before": "2026-01-01T00:00:00Z", "before_message_id": "older"},
	} {
		if _, err := executeHistoryRead(t, provider, args); err == nil {
			t.Errorf("accepted ambiguous cursor: %v", args)
		}
	}
}

func TestHistoryReadPostgresHiddenCursorIsNotExhaustion(t *testing.T) {
	provider, service, tx := historySearchPostgres(t)
	persistSearchFixture(t, service, "user", `{"role":"user","content":"older"}`)
	cursor := persistSearchFixture(t, service, "user", `{"role":"user","content":"newer"}`)
	if _, err := tx.Exec(context.Background(), `UPDATE bot_history_messages SET turn_visible=false WHERE id=$1`, cursor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := executeHistoryRead(t, provider, map[string]any{"before_message_id": cursor.ID}); err == nil {
		t.Fatal("hidden cursor must be reported as invalid, not an empty completed page")
	}
}

func TestHistoryReadLargeMetadataKeepsDiscoverableMessage(t *testing.T) {
	t.Parallel()
	msg := historyTestMessage(t, "message-large-metadata", searchTestSessionID, "user", "original text", time.Time{})
	msg.SenderDisplayName = strings.Repeat("sender", 10000)
	msg.Assets = []messagepkg.MessageAsset{{Name: strings.Repeat("asset", 10000)}}
	provider := NewHistoryProvider(nil, nil, &fakeHistoryMessageReader{latestMessages: []messagepkg.Message{msg}}, nil)
	out, err := executeHistoryRead(t, provider, map[string]any{"limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	rows := out["messages"].([]map[string]any)
	encoded, _ := json.Marshal(out)
	if len(encoded) > 32*1024 || len(rows) != 1 || rows[0]["id"] != msg.ID || rows[0]["metadata_truncated"] != true {
		t.Fatalf("message cannot be discovered within budget: %v", out)
	}
}

func TestHistoryReadPostgresExecutionRetainsCompactedEvidenceAndHonoursHiding(t *testing.T) {
	provider, service, tx := historySearchPostgres(t)
	persistSearchFixture(t, service, "user", `{"role":"user","content":"investigate"}`)
	call := persistSearchFixture(t, service, "assistant", `{"role":"assistant","content":[{"type":"tool-call","toolCallId":"call-evidence","toolName":"inspect","input":{"sample":"copper"}}]}`)
	result := persistSearchFixture(t, service, "tool", `{"role":"tool","content":[{"type":"tool-result","toolCallId":"call-evidence","toolName":"inspect","result":{"value":42,"unit":"mV"}}]}`)
	compactID := "00000000-0000-0000-0000-000000098199"
	if _, err := tx.Exec(context.Background(), `INSERT INTO bot_history_message_compacts(id,bot_id,session_id,status,summary) VALUES($1,$2,$3,'ok','diagnosis summary')`, compactID, searchTestBotID, searchTestSessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `UPDATE bot_history_messages SET compact_id=$1 WHERE id=$2 OR id=$3`, compactID, call.ID, result.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{call.ID, result.ID} {
		out, err := executeHistoryRead(t, provider, map[string]any{"message_id": id, "view": "execution"})
		if err != nil {
			t.Fatal(err)
		}
		rows := out["messages"].([]map[string]any)
		if len(rows) != 1 || rows[0]["turn_id"] == nil || !strings.Contains(rows[0]["content"].(string), "call-evidence") {
			t.Fatalf("compacted source lost evidence or linkage: %v", out)
		}
	}
	if _, err := tx.Exec(context.Background(), `UPDATE bot_history_messages SET turn_visible=false WHERE id=$1`, result.ID); err != nil {
		t.Fatal(err)
	}
	out, err := executeHistoryRead(t, provider, map[string]any{"message_id": result.ID, "view": "execution"})
	if err != nil || out["count"] != 0 {
		t.Fatalf("hidden source still readable: %v, %v", out, err)
	}
}

var _ HistoryMessageReader = (*messagepkg.DBService)(nil)
