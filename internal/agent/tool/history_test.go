package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	session "github.com/felinics/memoh/internal/chat/thread"
)

type fakeHistorySessionLister struct {
	sessions []session.Thread
}

func (f fakeHistorySessionLister) ListByBot(_ context.Context, _ string) ([]session.Thread, error) {
	return f.sessions, nil
}

type fakeHistoryMessageReader struct {
	latestSessionID string
	beforeSessionID string
	exactSessionID  string
	exactMessageID  string
	before          time.Time
	latestMessages  []messagepkg.Message
	beforeMessages  []messagepkg.Message
	exactMessage    messagepkg.Message
	exactErr        error
}

func (f *fakeHistoryMessageReader) ListLatestBySession(_ context.Context, sessionID string, _ int32) ([]messagepkg.Message, error) {
	f.latestSessionID = sessionID
	return f.latestMessages, nil
}

func (f *fakeHistoryMessageReader) ListBeforeBySession(_ context.Context, sessionID string, before time.Time, _ int32) ([]messagepkg.Message, error) {
	f.beforeSessionID = sessionID
	f.before = before
	return f.beforeMessages, nil
}

func (f *fakeHistoryMessageReader) GetByIDBySession(_ context.Context, sessionID string, messageID string) (messagepkg.Message, error) {
	f.exactSessionID = sessionID
	f.exactMessageID = messageID
	return f.exactMessage, f.exactErr
}

func TestHistoryProviderGetMessagesDefaultsToCurrentSession(t *testing.T) {
	t.Parallel()

	older := historyTestMessage(t, "msg-1", "session-current", "user", "hello", time.Date(2026, 6, 14, 9, 0, 0, 0, time.UTC))
	newer := historyTestMessage(t, "msg-2", "session-current", "assistant", "hi there", time.Date(2026, 6, 14, 9, 1, 0, 0, time.UTC))
	reader := &fakeHistoryMessageReader{latestMessages: []messagepkg.Message{newer, older}}
	provider := NewHistoryProvider(nil, nil, reader, nil)

	got, err := provider.execGetMessages(context.Background(), SessionContext{
		BotID:     "bot-1",
		SessionID: "session-current",
	}, map[string]any{"limit": 2})
	if err != nil {
		t.Fatalf("execGetMessages() error = %v", err)
	}
	if reader.latestSessionID != "session-current" {
		t.Fatalf("latest session id = %q, want session-current", reader.latestSessionID)
	}

	out := got.(map[string]any)
	messages := out["messages"].([]map[string]any)
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(messages))
	}
	if messages[0]["id"] != "msg-1" || messages[0]["text"] != "hello" {
		t.Fatalf("first message = %#v, want oldest message text", messages[0])
	}
	if messages[1]["id"] != "msg-2" || messages[1]["text"] != "hi there" {
		t.Fatalf("second message = %#v, want newest message text", messages[1])
	}
}

func TestHistoryProviderGetMessagesRejectsMissingSessionScope(t *testing.T) {
	t.Parallel()

	reader := &fakeHistoryMessageReader{}
	provider := NewHistoryProvider(nil, nil, reader, nil)
	if _, err := provider.execGetMessages(context.Background(), SessionContext{BotID: "bot-1"}, nil); err == nil {
		t.Fatal("execGetMessages() error = nil, want missing session scope error")
	}
}

func TestHistoryProviderGetMessagesBeforeUsesRequestedSession(t *testing.T) {
	t.Parallel()

	reader := &fakeHistoryMessageReader{
		beforeMessages: []messagepkg.Message{
			historyTestMessage(t, "msg-1", "session-other", "user", "before cursor", time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)),
		},
	}
	provider := NewHistoryProvider(nil, fakeHistorySessionLister{
		sessions: []session.Thread{{ID: "session-other", BotID: "bot-1", CreatedByUserID: "user-1"}},
	}, reader, nil)

	got, err := provider.execGetMessages(context.Background(), SessionContext{
		BotID:     "bot-1",
		SessionID: "session-current",
		UserID:    "user-1",
	}, map[string]any{
		"session_id": "session-other",
		"before":     "2026-06-14T09:00:00Z",
	})
	if err != nil {
		t.Fatalf("execGetMessages() error = %v", err)
	}
	if reader.beforeSessionID != "session-other" {
		t.Fatalf("before session id = %q, want session-other", reader.beforeSessionID)
	}
	if reader.before.IsZero() {
		t.Fatal("before cursor was not parsed")
	}

	out := got.(map[string]any)
	if out["session_id"] != "session-other" {
		t.Fatalf("session_id = %v, want session-other", out["session_id"])
	}
	messages := out["messages"].([]map[string]any)
	if messages[0]["text"] != "before cursor" {
		t.Fatalf("message text = %v, want before cursor", messages[0]["text"])
	}
}

func TestHistoryProviderGetMessagesResolvesExactSourceRef(t *testing.T) {
	t.Parallel()

	reader := &fakeHistoryMessageReader{exactMessage: historyTestMessage(
		t, "msg-source", "session-current", "user", "supporting detail", time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC),
	)}
	provider := NewHistoryProvider(nil, nil, reader, nil)
	got, err := provider.execGetMessages(context.Background(), SessionContext{
		BotID: "bot-1", SessionID: "session-current",
	}, map[string]any{"session_id": "session-current", "message_id": "msg-source"})
	if err != nil {
		t.Fatalf("execGetMessages() error = %v", err)
	}
	if reader.exactSessionID != "session-current" || reader.exactMessageID != "msg-source" {
		t.Fatalf("exact lookup = (%q, %q)", reader.exactSessionID, reader.exactMessageID)
	}
	messages := got.(map[string]any)["messages"].([]map[string]any)
	if len(messages) != 1 || messages[0]["text"] != "supporting detail" {
		t.Fatalf("exact messages = %v", messages)
	}
}

func TestHistoryProviderGetMessagesRejectsExactLookupWithBefore(t *testing.T) {
	t.Parallel()
	provider := NewHistoryProvider(nil, nil, &fakeHistoryMessageReader{}, nil)
	_, err := provider.execGetMessages(context.Background(), SessionContext{
		BotID: "bot-1", SessionID: "session-current",
	}, map[string]any{"message_id": "msg-source", "before": "2026-06-14T09:00:00Z"})
	if err == nil {
		t.Fatal("execGetMessages() error = nil, want ambiguous argument error")
	}
}

func TestHistoryProviderGetMessagesRejectsInaccessibleSessionOnSameBot(t *testing.T) {
	t.Parallel()

	reader := &fakeHistoryMessageReader{}
	provider := NewHistoryProvider(nil, fakeHistorySessionLister{
		sessions: []session.Thread{
			{ID: "session-current", BotID: "bot-1", RouteID: "route-alice"},
			{ID: "session-other", BotID: "bot-1", RouteID: "route-bob"},
		},
	}, reader, nil)

	_, err := provider.execGetMessages(context.Background(), SessionContext{
		BotID:     "bot-1",
		SessionID: "session-current",
	}, map[string]any{
		"session_id": "session-other",
	})
	if err == nil {
		t.Fatal("execGetMessages() error = nil, want session visibility error")
	}
}

func TestHistoryProviderGetMessagesAllowsSessionOnSameRoute(t *testing.T) {
	t.Parallel()

	reader := &fakeHistoryMessageReader{}
	provider := NewHistoryProvider(nil, fakeHistorySessionLister{
		sessions: []session.Thread{
			{ID: "session-current", BotID: "bot-1", RouteID: "route-private"},
			{ID: "session-previous", BotID: "bot-1", RouteID: "route-private"},
		},
	}, reader, nil)

	if _, err := provider.execGetMessages(context.Background(), SessionContext{
		BotID: "bot-1", SessionID: "session-current",
	}, map[string]any{"session_id": "session-previous"}); err != nil {
		t.Fatalf("execGetMessages() error = %v, want same-route session to be visible", err)
	}
	if reader.latestSessionID != "session-previous" {
		t.Fatalf("latest session id = %q, want session-previous", reader.latestSessionID)
	}
}

func TestHistoryProviderListSessionsFiltersOtherUsersAndRoutes(t *testing.T) {
	t.Parallel()

	provider := NewHistoryProvider(nil, fakeHistorySessionLister{
		sessions: []session.Thread{
			{ID: "session-current", BotID: "bot-1", RouteID: "route-current"},
			{ID: "session-same-route", BotID: "bot-1", RouteID: "route-current"},
			{ID: "session-same-user", BotID: "bot-1", CreatedByUserID: "user-1"},
			{ID: "session-bob", BotID: "bot-1", RouteID: "route-bob", CreatedByUserID: "user-2"},
		},
	}, nil, nil)

	got, err := provider.execListSessions(context.Background(), SessionContext{
		BotID: "bot-1", SessionID: "session-current", UserID: "user-1",
	}, nil)
	if err != nil {
		t.Fatalf("execListSessions() error = %v", err)
	}
	items := got.(map[string]any)["sessions"].([]map[string]any)
	if len(items) != 3 {
		t.Fatalf("visible sessions = %v, want current, same route, and same user", items)
	}
	for _, item := range items {
		if item["session_id"] == "session-bob" {
			t.Fatalf("inaccessible session leaked: %v", item)
		}
	}
}

func TestHistoryVisibilityUsesPersistedCurrentSessionOwner(t *testing.T) {
	t.Parallel()

	_, allowed, err := visibleHistorySessions(context.Background(), fakeHistorySessionLister{
		sessions: []session.Thread{
			{ID: "session-current", BotID: "bot-1", CreatedByUserID: "user-owner"},
			{ID: "session-owner", BotID: "bot-1", CreatedByUserID: "user-owner"},
			{ID: "session-context", BotID: "bot-1", CreatedByUserID: "user-context"},
		},
	}, SessionContext{BotID: "bot-1", SessionID: "session-current", UserID: "user-context"})
	if err != nil {
		t.Fatalf("visibleHistorySessions() error = %v", err)
	}
	if !historySessionVisible(allowed, "session-owner") {
		t.Fatal("persisted current-session owner should define the user scope")
	}
	if historySessionVisible(allowed, "session-context") {
		t.Fatal("request context must not override the persisted current-session owner")
	}
}

func TestExtractTextContentSummarizesAssistantToolCalls(t *testing.T) {
	t.Parallel()

	content, err := json.Marshal([]map[string]any{
		{"type": "reasoning", "text": "thinking"},
		{"type": "tool-call", "toolName": "read", "toolCallId": "call-1", "input": map[string]any{"path": "/tmp/a.txt"}},
		{"type": "tool-call", "toolName": "edit", "toolCallId": "call-2", "input": map[string]any{"path": "/tmp/a.txt"}},
	})
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}

	raw, err := json.Marshal(turn.ModelMessage{
		Role:    "assistant",
		Content: content,
	})
	if err != nil {
		t.Fatalf("marshal model message: %v", err)
	}

	got := extractTextContent(raw)
	want := "[tool_call: read, edit]"
	if got != want {
		t.Fatalf("extractTextContent() = %q, want %q", got, want)
	}
}

func historyTestMessage(t *testing.T, id, sessionID, role, text string, createdAt time.Time) messagepkg.Message {
	t.Helper()

	rawText, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("marshal text: %v", err)
	}
	content, err := json.Marshal(turn.ModelMessage{
		Role:    role,
		Content: rawText,
	})
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	return messagepkg.Message{
		ID:        id,
		BotID:     "bot-1",
		SessionID: sessionID,
		Role:      role,
		Content:   content,
		CreatedAt: createdAt,
	}
}

func TestExtractTextContentSummarizesToolResults(t *testing.T) {
	t.Parallel()

	content, err := json.Marshal([]map[string]any{
		{"type": "tool-result", "toolName": "search_messages", "toolCallId": "call-1", "result": map[string]any{"count": 3}},
	})
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}

	raw, err := json.Marshal(turn.ModelMessage{
		Role:    "tool",
		Content: content,
	})
	if err != nil {
		t.Fatalf("marshal model message: %v", err)
	}

	got := extractTextContent(raw)
	want := "[tool_result: search_messages]"
	if got != want {
		t.Fatalf("extractTextContent() = %q, want %q", got, want)
	}
}
