package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	session "github.com/memohai/memoh/internal/chat/thread"
)

type sessionContextLister struct {
	threads []session.Thread
}

func (l *sessionContextLister) ListByBot(context.Context, string) ([]session.Thread, error) {
	return l.threads, nil
}

type fakeSessionContextComposer struct {
	reqs   []SessionContextWindowRequest
	result SessionContextWindowResult
	err    error
}

func (c *fakeSessionContextComposer) ComposeSessionContextWindow(_ context.Context, req SessionContextWindowRequest) (SessionContextWindowResult, error) {
	c.reqs = append(c.reqs, req)
	return c.result, c.err
}

func sessionContextProvider(threads []session.Thread, composer SessionContextComposer) *HistoryProvider {
	p := NewHistoryProvider(nil, &sessionContextLister{threads: threads}, nil, nil)
	p.SetSessionContextComposer(composer)
	return p
}

func sessionContextTestThreads() []session.Thread {
	return []session.Thread{
		{ID: "sess-1", BotID: "bot-1", RouteID: "route-a", CreatedByUserID: "user-1"},
		{ID: "sess-2", BotID: "bot-1", RouteID: "route-a"},
		{ID: "sess-3", BotID: "bot-1", RouteID: "route-b", CreatedByUserID: "user-2"},
	}
}

func TestGetSessionContextRejectsInvisibleSession(t *testing.T) {
	t.Parallel()

	composer := &fakeSessionContextComposer{}
	p := sessionContextProvider(sessionContextTestThreads(), composer)
	sess := SessionContext{BotID: "bot-1", SessionID: "sess-1", UserID: "user-1"}

	_, err := p.execGetSessionContext(context.Background(), sess, map[string]any{"session_id": "sess-3"})
	if err == nil {
		t.Fatal("expected visibility rejection for a foreign-route session")
	}
	if !strings.Contains(err.Error(), "not accessible") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(composer.reqs) != 0 {
		t.Fatal("composer must not be reached for an invisible session")
	}
}

func TestGetSessionContextComposesVisibleSession(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	composer := &fakeSessionContextComposer{result: SessionContextWindowResult{
		Entries: []SessionContextEntry{
			{Kind: "message", Role: "user", Text: "hello", MessageID: "msg-9", SessionID: "sess-2", CreatedAt: createdAt},
			{Kind: "summary", Role: "user", Text: "earlier we picked pgvector", SessionID: "sess-2", CompactID: "compact-1"},
		},
		EstimatedTokens: 42,
		Truncated:       true,
	}}
	p := sessionContextProvider(sessionContextTestThreads(), composer)
	sess := SessionContext{BotID: "bot-1", SessionID: "sess-1", UserID: "user-1"}

	out, err := p.execGetSessionContext(context.Background(), sess, map[string]any{
		"session_id":        "sess-2",
		"around_message_id": "msg-9",
		"window_minutes":    120,
		"max_tokens":        2000,
	})
	if err != nil {
		t.Fatalf("execGetSessionContext error: %v", err)
	}
	if len(composer.reqs) != 1 {
		t.Fatalf("expected one compose call, got %d", len(composer.reqs))
	}
	req := composer.reqs[0]
	if req.BotID != "bot-1" || req.SessionID != "sess-2" || req.AnchorMessageID != "msg-9" || req.WindowMinutes != 120 || req.MaxTokens != 2000 {
		t.Fatalf("unexpected compose request: %+v", req)
	}

	result, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	if result["ok"] != true || result["session_id"] != "sess-2" || result["count"] != 2 {
		t.Fatalf("unexpected envelope: %+v", result)
	}
	if result["estimated_tokens"] != 42 || result["truncated"] != true {
		t.Fatalf("unexpected budget fields: %+v", result)
	}
	entries, ok := result["entries"].([]map[string]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("unexpected entries shape: %+v", result["entries"])
	}
	first := entries[0]
	if first["kind"] != "message" || first["role"] != "user" || first["text"] != "hello" {
		t.Fatalf("unexpected first entry: %+v", first)
	}
	if first["message_id"] != "msg-9" || first["session_id"] != "sess-2" {
		t.Fatalf("first entry refs = %v/%v, want msg-9/sess-2", first["message_id"], first["session_id"])
	}
	if _, present := first["created_at"]; !present {
		t.Fatal("expected created_at on message entry")
	}
	second := entries[1]
	if second["kind"] != "summary" || second["compact_id"] != "compact-1" {
		t.Fatalf("unexpected summary entry: %+v", second)
	}
	if _, present := second["message_id"]; present {
		t.Fatal("summary entry must not carry message_id")
	}
	if _, present := second["created_at"]; present {
		t.Fatal("summary entry without timestamp must omit created_at")
	}
}

func TestGetSessionContextDefaultsToCurrentSession(t *testing.T) {
	t.Parallel()

	composer := &fakeSessionContextComposer{}
	p := sessionContextProvider(sessionContextTestThreads(), composer)
	sess := SessionContext{BotID: "bot-1", SessionID: "sess-1", UserID: "user-1"}

	if _, err := p.execGetSessionContext(context.Background(), sess, map[string]any{}); err != nil {
		t.Fatalf("execGetSessionContext error: %v", err)
	}
	if len(composer.reqs) != 1 || composer.reqs[0].SessionID != "sess-1" {
		t.Fatalf("expected compose for current session, got %+v", composer.reqs)
	}
}

func TestGetSessionContextPropagatesComposerError(t *testing.T) {
	t.Parallel()

	composer := &fakeSessionContextComposer{err: errors.New("boom")}
	p := sessionContextProvider(sessionContextTestThreads(), composer)
	sess := SessionContext{BotID: "bot-1", SessionID: "sess-1", UserID: "user-1"}

	if _, err := p.execGetSessionContext(context.Background(), sess, map[string]any{}); err == nil {
		t.Fatal("expected composer error to propagate")
	}
}

func TestHistoryToolsGateSessionContextOnComposer(t *testing.T) {
	t.Parallel()

	withoutComposer := NewHistoryProvider(nil, &sessionContextLister{}, nil, nil)
	tools, err := withoutComposer.Tools(context.Background(), SessionContext{BotID: "bot-1"})
	if err != nil {
		t.Fatalf("Tools error: %v", err)
	}
	for _, tool := range tools {
		if tool.Name == ToolGetSessionContext().String() {
			t.Fatal("get_session_context must not register without a composer")
		}
	}

	withComposer := sessionContextProvider(nil, &fakeSessionContextComposer{})
	tools, err = withComposer.Tools(context.Background(), SessionContext{BotID: "bot-1"})
	if err != nil {
		t.Fatalf("Tools error: %v", err)
	}
	found := false
	for _, tool := range tools {
		if tool.Name == ToolGetSessionContext().String() {
			found = true
		}
	}
	if !found {
		t.Fatal("expected get_session_context to register with a composer")
	}
}

func TestHistoryUsageExplainsSessionContextDrillDown(t *testing.T) {
	t.Parallel()

	p := sessionContextProvider(nil, &fakeSessionContextComposer{})
	usage := p.Usage(context.Background(), SessionContext{BotID: "bot-1"}, availableToolsForTest(
		ToolListSessions(), ToolGetSessionContext(), ToolSearchMemory(),
	))
	if !strings.Contains(usage, ToolGetSessionContext().String()) {
		t.Fatalf("usage must mention get_session_context, got: %s", usage)
	}
	if !strings.Contains(usage, "source_refs") {
		t.Fatalf("usage must explain following search_memory source_refs, got: %s", usage)
	}

	withoutMemory := p.Usage(context.Background(), SessionContext{BotID: "bot-1"}, availableToolsForTest(
		ToolListSessions(), ToolGetSessionContext(),
	))
	if strings.Contains(withoutMemory, "source_refs") {
		t.Fatalf("usage must not reference source_refs when search_memory is unavailable, got: %s", withoutMemory)
	}
}
