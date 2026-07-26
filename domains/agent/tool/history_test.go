package tool

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	agentdomain "github.com/memohai/memoh/domains/agent"
	messagepkg "github.com/memohai/memoh/domains/agent/chat/message"
	session "github.com/memohai/memoh/domains/agent/chat/thread"
	toolpersistence "github.com/memohai/memoh/domains/agent/tool/persistence"
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
	before          time.Time
	latestMessages  []messagepkg.Message
	beforeMessages  []messagepkg.Message
}

type fakeHistorySearcher struct {
	filter  toolpersistence.HistorySearchFilter
	results []toolpersistence.HistorySearchResult
	err     error
}

func (f *fakeHistorySearcher) SearchHistory(_ context.Context, filter toolpersistence.HistorySearchFilter) ([]toolpersistence.HistorySearchResult, error) {
	f.filter = filter
	return f.results, f.err
}

func (f *fakeHistoryMessageReader) ListLatest(_ context.Context, _ string, _ int32) ([]messagepkg.Message, error) {
	return f.latestMessages, nil
}

func (f *fakeHistoryMessageReader) ListBefore(_ context.Context, _ string, before time.Time, _ int32) ([]messagepkg.Message, error) {
	f.before = before
	return f.beforeMessages, nil
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

func TestHistoryProviderGetMessagesBeforeUsesRequestedSession(t *testing.T) {
	t.Parallel()

	reader := &fakeHistoryMessageReader{
		beforeMessages: []messagepkg.Message{
			historyTestMessage(t, "msg-1", "session-other", "user", "before cursor", time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)),
		},
	}
	provider := NewHistoryProvider(nil, fakeHistorySessionLister{
		sessions: []session.Thread{{ID: "session-other", BotID: "bot-1"}},
	}, reader, nil)

	got, err := provider.execGetMessages(context.Background(), SessionContext{
		BotID:     "bot-1",
		SessionID: "session-current",
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

func TestHistoryProviderGetMessagesRejectsSessionOutsideBot(t *testing.T) {
	t.Parallel()

	reader := &fakeHistoryMessageReader{}
	provider := NewHistoryProvider(nil, fakeHistorySessionLister{
		sessions: []session.Thread{{ID: "session-current", BotID: "bot-1"}},
	}, reader, nil)

	_, err := provider.execGetMessages(context.Background(), SessionContext{
		BotID:     "bot-1",
		SessionID: "session-current",
	}, map[string]any{
		"session_id": "session-other",
	})
	if err == nil {
		t.Fatal("execGetMessages() error = nil, want session ownership error")
	}
}

func TestHistoryProviderSearchMessagesPassesDomainFilterAndFormatsResults(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 6, 14, 9, 30, 0, 0, time.UTC)
	searcher := &fakeHistorySearcher{
		results: []toolpersistence.HistorySearchResult{{
			ID:        "message-1",
			SessionID: "session-1",
			ContactID: "contact-1",
			Role:      "user",
			Content:   historyTestMessage(t, "message-1", "session-1", "user", "matching text", createdAt).Content,
			CreatedAt: createdAt,
			Sender:    "Ada",
			Platform:  "telegram",
		}},
	}
	provider := NewHistoryProvider(nil, nil, nil, searcher)

	got, err := provider.execSearchMessages(context.Background(), SessionContext{
		BotID: "bot-1",
	}, map[string]any{
		"session_id": "session-1",
		"contact_id": "contact-1",
		"role":       "user",
		"keyword":    "matching",
		"start_time": "2026-06-01T00:00:00Z",
		"end_time":   "2026-06-30T00:00:00Z",
		"limit":      100,
	})
	if err != nil {
		t.Fatalf("execSearchMessages() error = %v", err)
	}

	wantStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	if searcher.filter.BotID != "bot-1" ||
		searcher.filter.SessionID != "session-1" ||
		searcher.filter.ContactID != "contact-1" ||
		searcher.filter.Role != "user" ||
		searcher.filter.Keyword != "matching" ||
		!searcher.filter.StartTime.Equal(wantStart) ||
		!searcher.filter.EndTime.Equal(wantEnd) ||
		searcher.filter.Limit != 100 {
		t.Fatalf("search filter = %#v, want all requested filters", searcher.filter)
	}

	out := got.(map[string]any)
	messages := out["messages"].([]map[string]any)
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(messages))
	}
	message := messages[0]
	if message["id"] != "message-1" ||
		message["session_id"] != "session-1" ||
		message["contact_id"] != "contact-1" ||
		message["role"] != "user" ||
		message["text"] != "matching text" ||
		message["sender"] != "Ada" ||
		message["platform"] != "telegram" ||
		message["created_at"] != "2026-06-14T09:30:00Z" {
		t.Fatalf("formatted message = %#v", message)
	}
}

func TestHistoryProviderSearchMessagesPreservesDefaults(t *testing.T) {
	t.Parallel()

	searcher := &fakeHistorySearcher{}
	provider := NewHistoryProvider(nil, nil, nil, searcher)
	before := time.Now().UTC().AddDate(0, 0, -defaultMaxLookbackDays)

	_, err := provider.execSearchMessages(context.Background(), SessionContext{
		BotID: "bot-1",
	}, map[string]any{"limit": 201})
	if err != nil {
		t.Fatalf("execSearchMessages() error = %v", err)
	}
	after := time.Now().UTC().AddDate(0, 0, -defaultMaxLookbackDays)

	if searcher.filter.Limit != 50 {
		t.Fatalf("limit = %d, want default 50", searcher.filter.Limit)
	}
	if searcher.filter.StartTime.Before(before) || searcher.filter.StartTime.After(after) {
		t.Fatalf("start time = %v, want seven-day default between %v and %v", searcher.filter.StartTime, before, after)
	}
}

func TestHistoryProviderSearchMessagesIgnoresInvalidExplicitTime(t *testing.T) {
	t.Parallel()

	searcher := &fakeHistorySearcher{}
	provider := NewHistoryProvider(nil, nil, nil, searcher)

	_, err := provider.execSearchMessages(context.Background(), SessionContext{
		BotID: "bot-1",
	}, map[string]any{
		"start_time": "not-a-time",
		"end_time":   "not-a-time",
	})
	if err != nil {
		t.Fatalf("execSearchMessages() error = %v", err)
	}
	if !searcher.filter.StartTime.IsZero() || !searcher.filter.EndTime.IsZero() {
		t.Fatalf("times = (%v, %v), want zero values", searcher.filter.StartTime, searcher.filter.EndTime)
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

	raw, err := json.Marshal(agentdomain.ModelMessage{
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
	content, err := json.Marshal(agentdomain.ModelMessage{
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

	raw, err := json.Marshal(agentdomain.ModelMessage{
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
