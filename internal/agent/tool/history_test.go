package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
	"github.com/felinics/memoh/internal/agent/toolexec"
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
	provider := NewHistoryProvider(nil, nil, reader, nil, ToolOutputLimit{})

	got, err := provider.execGetMessages(context.Background(), SessionContext{
		BotID:     "bot-1",
		SessionID: "session-current",
	}, getMessagesArgs{Limit: intPtr(2)})
	if err != nil {
		t.Fatalf("execGetMessages() error = %v", err)
	}
	if reader.latestSessionID != "session-current" {
		t.Fatalf("latest session id = %q, want session-current", reader.latestSessionID)
	}

	out := got.(map[string]any)
	messages := asMapSlice(t, out["messages"])
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
	provider := NewHistoryProvider(nil, nil, reader, nil, ToolOutputLimit{})
	if _, err := provider.execGetMessages(context.Background(), SessionContext{BotID: "bot-1"}, getMessagesArgs{}); err == nil {
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
	}, reader, nil, ToolOutputLimit{})

	got, err := provider.execGetMessages(context.Background(), SessionContext{
		BotID:     "bot-1",
		SessionID: "session-current",
		UserID:    "user-1",
	}, getMessagesArgs{SessionID: "session-other", Before: "2026-06-14T09:00:00Z"})
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
	messages := asMapSlice(t, out["messages"])
	if messages[0]["text"] != "before cursor" {
		t.Fatalf("message text = %v, want before cursor", messages[0]["text"])
	}
}

func TestHistoryProviderGetMessagesResolvesExactSourceRef(t *testing.T) {
	t.Parallel()

	reader := &fakeHistoryMessageReader{exactMessage: historyTestMessage(
		t, "msg-source", "session-current", "user", "supporting detail", time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC),
	)}
	provider := NewHistoryProvider(nil, nil, reader, nil, ToolOutputLimit{})
	got, err := provider.execGetMessages(context.Background(), SessionContext{
		BotID: "bot-1", SessionID: "session-current",
	}, getMessagesArgs{SessionID: "session-current", MessageID: "msg-source"})
	if err != nil {
		t.Fatalf("execGetMessages() error = %v", err)
	}
	if reader.exactSessionID != "session-current" || reader.exactMessageID != "msg-source" {
		t.Fatalf("exact lookup = (%q, %q)", reader.exactSessionID, reader.exactMessageID)
	}
	messages := asMapSlice(t, got.(map[string]any)["messages"])
	if len(messages) != 1 || messages[0]["text"] != "supporting detail" {
		t.Fatalf("exact messages = %v", messages)
	}
}

func TestHistoryProviderGetMessagesRejectsExactLookupWithBefore(t *testing.T) {
	t.Parallel()
	provider := NewHistoryProvider(nil, nil, &fakeHistoryMessageReader{}, nil, ToolOutputLimit{})
	_, err := provider.execGetMessages(context.Background(), SessionContext{
		BotID: "bot-1", SessionID: "session-current",
	}, getMessagesArgs{MessageID: "msg-source", Before: "2026-06-14T09:00:00Z"})
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
	}, reader, nil, ToolOutputLimit{})

	_, err := provider.execGetMessages(context.Background(), SessionContext{
		BotID:     "bot-1",
		SessionID: "session-current",
	}, getMessagesArgs{SessionID: "session-other"})
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
	}, reader, nil, ToolOutputLimit{})

	if _, err := provider.execGetMessages(context.Background(), SessionContext{
		BotID: "bot-1", SessionID: "session-current",
	}, getMessagesArgs{SessionID: "session-previous"}); err != nil {
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
	}, nil, nil, ToolOutputLimit{})

	got, err := provider.execListSessions(context.Background(), SessionContext{
		BotID: "bot-1", SessionID: "session-current", UserID: "user-1",
	}, listSessionsArgs{})
	if err != nil {
		t.Fatalf("execListSessions() error = %v", err)
	}
	items := asMapSlice(t, got.(map[string]any)["sessions"])
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

const historyRefMessageID = "0190c3c4-5d2e-7b6a-9f10-2b3c4d5e6f70"

func historyToolRow(t *testing.T, parts ...map[string]any) messagepkg.Message {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"role": "tool", "content": parts})
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	return messagepkg.Message{ID: historyRefMessageID, SessionID: "session-current", Role: "tool", Content: raw}
}

func historyToolResultPart(name string, result any) map[string]any {
	return map[string]any{"type": "tool-result", "toolCallId": "call-" + name, "toolName": name, "result": result}
}

func TestHistoryProviderGetMessagesRefPagesStoredToolResult(t *testing.T) {
	t.Parallel()
	row := historyToolRow(t,
		map[string]any{"type": "text", "text": "note"},
		historyToolResultPart("exec", "hello wörld"),
		historyToolResultPart("web_search", map[string]any{"query": "a<b", "count": 2}),
		historyToolResultPart("write", nil),
	)
	for _, tc := range []struct {
		name               string
		ref                string
		offset, maxBytes   *int
		content            string
		start, total, next int
	}{
		{name: "first page", ref: ":1", maxBytes: intPtr(5), content: "hello", total: 12, next: 5},
		{name: "page stops before a split rune", ref: ":1", offset: intPtr(5), maxBytes: intPtr(3), content: " w", start: 5, total: 12, next: 7},
		{name: "offset inside a rune starts at that rune", ref: ":1", offset: intPtr(8), content: "örld", start: 7, total: 12},
		{name: "offset at the end", ref: ":1", offset: intPtr(12), start: 12, total: 12},
		{name: "json result reads as its canonical document", ref: ":2", content: `{"count":2,"query":"a<b"}`, total: 25},
		{name: "empty result", ref: ":3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reader := &fakeHistoryMessageReader{exactMessage: row}
			got, err := NewHistoryProvider(nil, nil, reader, nil, ToolOutputLimit{}).execGetMessages(context.Background(),
				SessionContext{BotID: "bot-1", SessionID: "session-current"},
				getMessagesArgs{Ref: historyRefMessageID + tc.ref, Offset: tc.offset, MaxBytes: tc.maxBytes})
			if err != nil {
				t.Fatalf("execGetMessages() error = %v", err)
			}
			if reader.exactSessionID != "session-current" || reader.exactMessageID != historyRefMessageID {
				t.Fatalf("read (%q, %q), want the ref's row in the current session", reader.exactSessionID, reader.exactMessageID)
			}
			page := got.(map[string]any)
			next, hasNext := page["next_offset"]
			if page["content"] != tc.content || page["offset"] != tc.start || page["total_bytes"] != tc.total || hasNext != (tc.next > 0) || (hasNext && next != tc.next) {
				t.Fatalf("page = %v, want content %q offset %d total %d next %d", page, tc.content, tc.start, tc.total, tc.next)
			}
		})
	}
}

func TestHistoryProviderGetMessagesRefRejectsInvalidReads(t *testing.T) {
	t.Parallel()
	row := historyToolRow(t,
		map[string]any{"type": "text", "text": "note"},
		map[string]any{"type": "tool-call", "toolCallId": "call-exec", "toolName": "exec", "input": map[string]any{}},
		historyToolResultPart("exec", "out"),
	)
	ref := historyRefMessageID + ":2"
	for _, tc := range []struct {
		name    string
		args    getMessagesArgs
		readErr error
		want    string
	}{
		{name: "missing part index", args: getMessagesArgs{Ref: historyRefMessageID}, want: "ref must be"},
		{name: "signed part index", args: getMessagesArgs{Ref: historyRefMessageID + ":+2"}, want: "ref must be"},
		{name: "message id is not a uuid", args: getMessagesArgs{Ref: "msg-1:2"}, want: "ref must be"},
		{name: "ref with message limit", args: getMessagesArgs{Ref: ref, Limit: intPtr(30)}, want: "cannot be combined"},
		{name: "offset without ref", args: getMessagesArgs{Offset: intPtr(3)}, want: "require ref"},
		{name: "max_bytes without ref", args: getMessagesArgs{MaxBytes: intPtr(3)}, want: "require ref"},
		{name: "negative offset", args: getMessagesArgs{Ref: ref, Offset: intPtr(-1)}, want: "offset must be"},
		{name: "zero max_bytes", args: getMessagesArgs{Ref: ref, MaxBytes: intPtr(0)}, want: "max_bytes must be"},
		{name: "offset past the end", args: getMessagesArgs{Ref: ref, Offset: intPtr(4)}, want: "past the end"},
		{name: "another session", args: getMessagesArgs{Ref: ref, SessionID: "session-other"}, want: "only the current session"},
		{name: "row outside the current session", args: getMessagesArgs{Ref: ref}, readErr: pgx.ErrNoRows, want: "not in the current session"},
		{name: "part out of range", args: getMessagesArgs{Ref: historyRefMessageID + ":3"}, want: "no part 3"},
		{name: "text part", args: getMessagesArgs{Ref: historyRefMessageID + ":0"}, want: "not a tool result"},
		{name: "tool call part", args: getMessagesArgs{Ref: historyRefMessageID + ":1"}, want: "not a tool result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reader := &fakeHistoryMessageReader{exactMessage: row, exactErr: tc.readErr}
			_, err := NewHistoryProvider(nil, nil, reader, nil, ToolOutputLimit{}).execGetMessages(context.Background(),
				SessionContext{BotID: "bot-1", SessionID: "session-current"}, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("execGetMessages() error = %v, want it to mention %q", err, tc.want)
			}
			if reader.exactSessionID != "" && reader.exactSessionID != "session-current" {
				t.Fatalf("read session %q, want only the current session", reader.exactSessionID)
			}
		})
	}
}

// Every page goes through the output-limit wrapper the runtime applies. A
// page the wrapper had to elide would lose the bytes between offset and
// next_offset, and a page cut far below the limit costs extra calls.
func TestHistoryProviderGetMessagesRefPagesSurviveTheOutputLimit(t *testing.T) {
	t.Parallel()
	original := strings.Repeat(`"quoted" <tag> & ünï 你好`+"\n", 200)
	encoded := len(contextlimit.MarshalString(original))
	for _, tc := range []struct {
		name     string
		limit    ToolOutputLimit
		maxPages int
	}{
		{name: "line limit binds at 15 newlines a page", limit: ToolOutputLimit{MaxBytes: 4096, MaxLines: 16}, maxPages: 14},
		{name: "escaped bytes bind at half the limit or more a page", limit: ToolOutputLimit{MaxBytes: 1024, MaxLines: 2000}, maxPages: 2 * encoded / 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reader := &fakeHistoryMessageReader{exactMessage: historyToolRow(t, historyToolResultPart("exec", original))}
			tools, err := NewHistoryProvider(nil, nil, reader, nil, tc.limit).Tools(context.Background(), SessionContext{BotID: "bot-1", SessionID: "session-current"})
			if err != nil {
				t.Fatalf("Tools() error = %v", err)
			}
			if pages := readToolResultRef(t, WrapToolOutputLimits(tools, tc.limit)[0], "ref="+historyRefMessageID+":0", original); pages > tc.maxPages {
				t.Fatalf("pages = %d, want at most %d", pages, tc.maxPages)
			}
		})
	}
}

// readToolResultRef follows next_offset from offset 0 and returns the page
// count once the pages have reassembled want.
func readToolResultRef(t *testing.T, tool toolexec.Tool, ref, want string) int {
	t.Helper()
	var read strings.Builder
	for pages := 1; pages <= len(want)+1; pages++ {
		out, err := tool.Execute(&toolexec.ToolExecContext{Context: context.Background(), ToolName: tool.Name},
			toolexec.ArgumentsFromValue(map[string]any{"ref": ref, "offset": read.Len()}))
		var page struct {
			Content string `json:"content"`
			Offset  int    `json:"offset"`
			Total   int    `json:"total_bytes"`
			Next    *int   `json:"next_offset"`
		}
		if err != nil || json.Unmarshal(out.JSON, &page) != nil || page.Offset != read.Len() || page.Total != len(want) {
			t.Fatalf("page at offset %d = %s, %v; want that offset of %d bytes", read.Len(), out.String(), err, len(want))
		}
		read.WriteString(page.Content)
		if page.Next == nil {
			if read.String() != want {
				t.Fatalf("%d pages reassemble %d bytes that differ from the %d-byte original", pages, read.Len(), len(want))
			}
			return pages
		}
		if *page.Next != read.Len() {
			t.Fatalf("next_offset = %d, want the %d bytes read so far", *page.Next, read.Len())
		}
	}
	t.Fatalf("paging did not finish within %d pages", len(want)+1)
	return 0
}

func intPtr(v int) *int { return &v }
