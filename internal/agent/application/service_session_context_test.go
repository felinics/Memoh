package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	historyfrag "github.com/memohai/memoh/internal/agent/context/history"
	tools "github.com/memohai/memoh/internal/agent/tool"
	"github.com/memohai/memoh/internal/agent/turn"
	messagepkg "github.com/memohai/memoh/internal/chat/message"
	session "github.com/memohai/memoh/internal/chat/thread"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
)

type composeWindowMessageService struct {
	recordingMessageService
	msgs          []messagepkg.Message
	byID          map[string]messagepkg.Message
	byIDErr       error
	sinceCalls    []time.Time
	betweenCalls  [][2]time.Time
	betweenLimits []int32
}

func (s *composeWindowMessageService) ListActiveSinceBySession(_ context.Context, _ string, since time.Time) ([]messagepkg.Message, error) {
	s.sinceCalls = append(s.sinceCalls, since)
	out := make([]messagepkg.Message, 0, len(s.msgs))
	for _, m := range s.msgs {
		if !m.CreatedAt.Before(since) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *composeWindowMessageService) ListActiveBetweenBySession(_ context.Context, _ string, start, end time.Time, limit int32) ([]messagepkg.Message, error) {
	s.betweenCalls = append(s.betweenCalls, [2]time.Time{start, end})
	s.betweenLimits = append(s.betweenLimits, limit)
	out := make([]messagepkg.Message, 0, len(s.msgs))
	for _, m := range s.msgs {
		if !m.CreatedAt.Before(start) && !m.CreatedAt.After(end) {
			out = append(out, m)
		}
	}
	if limit > 0 && len(out) > int(limit) {
		out = out[len(out)-int(limit):]
	}
	return out, nil
}

func (s *composeWindowMessageService) GetByIDBySession(_ context.Context, _ string, messageID string) (messagepkg.Message, error) {
	msg, ok := s.byID[messageID]
	if !ok {
		if s.byIDErr != nil {
			return messagepkg.Message{}, s.byIDErr
		}
		return messagepkg.Message{}, errors.New("message not found")
	}
	return msg, nil
}

type composeWindowSessionService struct {
	thread session.Thread
	err    error
}

func (s *composeWindowSessionService) Get(context.Context, string) (session.Thread, error) {
	return s.thread, s.err
}

func (s *composeWindowSessionService) UpdateTitle(context.Context, string, string) (session.Thread, error) {
	return s.thread, nil
}

func (s *composeWindowSessionService) UpdateMetadata(context.Context, string, map[string]any) (session.Thread, error) {
	return s.thread, nil
}

func composeWindowMessage(t *testing.T, id, sessionID, role, text string, at time.Time) messagepkg.Message {
	t.Helper()
	m := persistedHistoryMessage(t, id, role, text)
	m.SessionID = sessionID
	m.CreatedAt = at
	return m
}

func composeWindowService(msgs *composeWindowMessageService, thread session.Thread) *Service {
	return &Service{
		messageService: msgs,
		sessionService: &composeWindowSessionService{thread: thread},
	}
}

func TestComposeSessionContextWindowBasic(t *testing.T) {
	t.Parallel()

	base := time.Now().UTC().Add(-2 * time.Hour)
	msgs := &composeWindowMessageService{msgs: []messagepkg.Message{
		composeWindowMessage(t, "msg-1", "sess-1", "user", "how do we deploy?", base),
		composeWindowMessage(t, "msg-2", "sess-1", "assistant", "with mise run deploy", base.Add(time.Minute)),
		composeWindowMessage(t, "msg-3", "sess-1", "user", "thanks", base.Add(2*time.Minute)),
	}}
	svc := composeWindowService(msgs, session.Thread{ID: "sess-1", BotID: "bot-1"})

	result, err := svc.ComposeSessionContextWindow(context.Background(), tools.SessionContextWindowRequest{
		BotID:     "bot-1",
		SessionID: "sess-1",
		MaxTokens: 8000,
	})
	if err != nil {
		t.Fatalf("ComposeSessionContextWindow error: %v", err)
	}
	if len(result.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(result.Entries), result.Entries)
	}
	first := result.Entries[0]
	if first.Kind != "message" || first.Role != "user" || first.Text != "how do we deploy?" {
		t.Fatalf("unexpected first entry: %+v", first)
	}
	if first.MessageID != "msg-1" || first.SessionID != "sess-1" {
		t.Fatalf("first entry refs = %q/%q, want msg-1/sess-1", first.MessageID, first.SessionID)
	}
	if first.CreatedAt.IsZero() {
		t.Fatal("expected entry CreatedAt to be set")
	}
	if result.Entries[2].MessageID != "msg-3" {
		t.Fatalf("expected ascending order, last = %+v", result.Entries[2])
	}
	if result.EstimatedTokens <= 0 {
		t.Fatalf("expected positive estimated tokens, got %d", result.EstimatedTokens)
	}
	if result.Truncated {
		t.Fatal("expected no truncation for small window")
	}
}

func TestComposeSessionContextWindowRejectsForeignBot(t *testing.T) {
	t.Parallel()

	msgs := &composeWindowMessageService{}
	svc := composeWindowService(msgs, session.Thread{ID: "sess-1", BotID: "bot-other"})

	_, err := svc.ComposeSessionContextWindow(context.Background(), tools.SessionContextWindowRequest{
		BotID:     "bot-1",
		SessionID: "sess-1",
	})
	if err == nil {
		t.Fatal("expected error for session belonging to another bot")
	}
	if len(msgs.sinceCalls)+len(msgs.betweenCalls) != 0 {
		t.Fatal("expected no history reads after ownership rejection")
	}
}

func TestComposeSessionContextWindowSessionLookupError(t *testing.T) {
	t.Parallel()

	svc := &Service{
		messageService: &composeWindowMessageService{},
		sessionService: &composeWindowSessionService{err: errors.New("boom")},
	}
	_, err := svc.ComposeSessionContextWindow(context.Background(), tools.SessionContextWindowRequest{
		BotID:     "bot-1",
		SessionID: "sess-1",
	})
	if err == nil {
		t.Fatal("expected session lookup error to propagate")
	}
}

func TestComposeSessionContextWindowAnchorCentersWindow(t *testing.T) {
	t.Parallel()

	anchorAt := time.Now().UTC().Add(-30 * 24 * time.Hour)
	anchor := composeWindowMessage(t, "msg-anchor", "sess-1", "user", "the detail we need", anchorAt)
	msgs := &composeWindowMessageService{
		msgs: []messagepkg.Message{
			composeWindowMessage(t, "msg-old", "sess-1", "user", "far before", anchorAt.Add(-2*time.Hour)),
			composeWindowMessage(t, "msg-before", "sess-1", "user", "just before", anchorAt.Add(-10*time.Minute)),
			anchor,
			composeWindowMessage(t, "msg-after", "sess-1", "assistant", "just after", anchorAt.Add(10*time.Minute)),
			composeWindowMessage(t, "msg-late", "sess-1", "user", "far after", anchorAt.Add(2*time.Hour)),
		},
		byID: map[string]messagepkg.Message{"msg-anchor": anchor},
	}
	svc := composeWindowService(msgs, session.Thread{ID: "sess-1", BotID: "bot-1"})

	result, err := svc.ComposeSessionContextWindow(context.Background(), tools.SessionContextWindowRequest{
		BotID:           "bot-1",
		SessionID:       "sess-1",
		AnchorMessageID: "msg-anchor",
		WindowMinutes:   60,
		MaxTokens:       8000,
	})
	if err != nil {
		t.Fatalf("ComposeSessionContextWindow error: %v", err)
	}
	if len(msgs.betweenCalls) != 1 || len(msgs.sinceCalls) != 0 {
		t.Fatalf("expected exactly one between call, got between=%d since=%d", len(msgs.betweenCalls), len(msgs.sinceCalls))
	}
	window := msgs.betweenCalls[0]
	if !window[0].Equal(anchorAt.Add(-30*time.Minute)) || !window[1].Equal(anchorAt.Add(30*time.Minute)) {
		t.Fatalf("window = [%v, %v], want centered ±30m around anchor", window[0], window[1])
	}
	ids := make([]string, 0, len(result.Entries))
	for _, e := range result.Entries {
		ids = append(ids, e.MessageID)
	}
	if strings.Join(ids, ",") != "msg-before,msg-anchor,msg-after" {
		t.Fatalf("entries = %v, want the centered window only", ids)
	}
}

func TestComposeSessionContextWindowAnchorNotFound(t *testing.T) {
	t.Parallel()

	msgs := &composeWindowMessageService{byID: map[string]messagepkg.Message{}}
	svc := composeWindowService(msgs, session.Thread{ID: "sess-1", BotID: "bot-1"})

	_, err := svc.ComposeSessionContextWindow(context.Background(), tools.SessionContextWindowRequest{
		BotID:           "bot-1",
		SessionID:       "sess-1",
		AnchorMessageID: "missing",
	})
	if err == nil {
		t.Fatal("expected error for unknown anchor message")
	}
}

func TestComposeSessionContextWindowDefaultsAndClamp(t *testing.T) {
	t.Parallel()

	msgs := &composeWindowMessageService{}
	svc := composeWindowService(msgs, session.Thread{ID: "sess-1", BotID: "bot-1"})

	if _, err := svc.ComposeSessionContextWindow(context.Background(), tools.SessionContextWindowRequest{
		BotID:     "bot-1",
		SessionID: "sess-1",
	}); err != nil {
		t.Fatalf("ComposeSessionContextWindow error: %v", err)
	}
	if len(msgs.betweenCalls) != 1 || len(msgs.sinceCalls) != 0 {
		t.Fatalf("expected one bounded between call, got between=%d since=%d", len(msgs.betweenCalls), len(msgs.sinceCalls))
	}
	gotWindow := msgs.betweenCalls[0][1].Sub(msgs.betweenCalls[0][0])
	if gotWindow < 23*time.Hour || gotWindow > 25*time.Hour {
		t.Fatalf("default window = %v, want ~24h", gotWindow)
	}
	if endLag := time.Since(msgs.betweenCalls[0][1]); endLag < 0 || endLag > 2*time.Minute {
		t.Fatalf("window end should be ~now, lag = %v", endLag)
	}
	if msgs.betweenLimits[0] <= 0 {
		t.Fatalf("expected a positive row cap, got %d", msgs.betweenLimits[0])
	}

	if _, err := svc.ComposeSessionContextWindow(context.Background(), tools.SessionContextWindowRequest{
		BotID:         "bot-1",
		SessionID:     "sess-1",
		WindowMinutes: 1_000_000,
	}); err != nil {
		t.Fatalf("ComposeSessionContextWindow error: %v", err)
	}
	gotWindow = msgs.betweenCalls[1][1].Sub(msgs.betweenCalls[1][0])
	if gotWindow < 6*24*time.Hour || gotWindow > 8*24*time.Hour {
		t.Fatalf("clamped window = %v, want ~7d", gotWindow)
	}
}

func TestComposeSessionContextWindowTokenBudgetTruncates(t *testing.T) {
	t.Parallel()

	base := time.Now().UTC().Add(-time.Hour)
	long := strings.Repeat("deployment detail ", 200)
	msgs := &composeWindowMessageService{msgs: []messagepkg.Message{
		composeWindowMessage(t, "msg-1", "sess-1", "user", long, base),
		composeWindowMessage(t, "msg-2", "sess-1", "assistant", long, base.Add(time.Minute)),
		composeWindowMessage(t, "msg-3", "sess-1", "user", long, base.Add(2*time.Minute)),
	}}
	svc := composeWindowService(msgs, session.Thread{ID: "sess-1", BotID: "bot-1"})

	result, err := svc.ComposeSessionContextWindow(context.Background(), tools.SessionContextWindowRequest{
		BotID:     "bot-1",
		SessionID: "sess-1",
		MaxTokens: 1000,
	})
	if err != nil {
		t.Fatalf("ComposeSessionContextWindow error: %v", err)
	}
	if !result.Truncated {
		t.Fatal("expected truncation under a tight token budget")
	}
	if len(result.Entries) == 0 || len(result.Entries) >= 3 {
		t.Fatalf("expected a strict subset of entries, got %d", len(result.Entries))
	}
	if last := result.Entries[len(result.Entries)-1]; last.MessageID != "msg-3" {
		t.Fatalf("expected newest message to survive trimming, got %+v", last)
	}
}

func TestComposeSessionContextWindowMaxTokensCap(t *testing.T) {
	t.Parallel()

	base := time.Now().UTC().Add(-time.Hour)
	long := strings.Repeat("deployment detail ", 1200)
	messages := make([]messagepkg.Message, 0, 5)
	for i := range 5 {
		messages = append(messages, composeWindowMessage(t, fmt.Sprintf("msg-%d", i+1), "sess-1", "user", long, base.Add(time.Duration(i)*time.Minute)))
	}
	msgs := &composeWindowMessageService{msgs: messages}
	svc := composeWindowService(msgs, session.Thread{ID: "sess-1", BotID: "bot-1"})

	result, err := svc.ComposeSessionContextWindow(context.Background(), tools.SessionContextWindowRequest{
		BotID:     "bot-1",
		SessionID: "sess-1",
		MaxTokens: 1_000_000,
	})
	if err != nil {
		t.Fatalf("ComposeSessionContextWindow error: %v", err)
	}
	if !result.Truncated {
		t.Fatal("expected the 16k token ceiling to hold even for an absurd max_tokens")
	}
}

func TestComposeSessionContextWindowAnchorNoRowsError(t *testing.T) {
	t.Parallel()

	msgs := &composeWindowMessageService{byID: map[string]messagepkg.Message{}, byIDErr: pgx.ErrNoRows}
	svc := composeWindowService(msgs, session.Thread{ID: "sess-1", BotID: "bot-1"})

	_, err := svc.ComposeSessionContextWindow(context.Background(), tools.SessionContextWindowRequest{
		BotID:           "bot-1",
		SessionID:       "sess-1",
		AnchorMessageID: "gone",
	})
	if err == nil || !strings.Contains(err.Error(), "around_message_id") {
		t.Fatalf("expected stable around_message_id error, got %v", err)
	}
}

func TestComposeSessionContextWindowSyntheticMarkersStayInternal(t *testing.T) {
	t.Parallel()

	base := time.Now().UTC().Add(-time.Hour)
	long := strings.Repeat("deployment detail ", 80)
	withTarget := composeWindowMessage(t, "msg-2", "sess-1", "assistant", long, base.Add(time.Minute))
	withTarget.Metadata = map[string]any{
		"execution_location": map[string]any{"target_id": "ws-1", "kind": "container", "name": "Dev"},
	}
	msgs := &composeWindowMessageService{msgs: []messagepkg.Message{
		composeWindowMessage(t, "msg-1", "sess-1", "user", long, base),
		withTarget,
	}}
	svc := composeWindowService(msgs, session.Thread{ID: "sess-1", BotID: "bot-1"})

	result, err := svc.ComposeSessionContextWindow(context.Background(), tools.SessionContextWindowRequest{
		BotID:     "bot-1",
		SessionID: "sess-1",
		MaxTokens: 500,
	})
	if err != nil {
		t.Fatalf("ComposeSessionContextWindow error: %v", err)
	}
	if len(result.Entries) == 0 {
		t.Fatal("expected the newest message to survive the budget")
	}
	for _, entry := range result.Entries {
		if entry.Kind == "message" && entry.MessageID == "" {
			t.Fatalf("synthetic marker leaked into entries: %+v", entry)
		}
	}
	if !result.Truncated {
		t.Fatal("expected truncation to be reported despite injected workspace markers")
	}

	untrimmed, err := svc.ComposeSessionContextWindow(context.Background(), tools.SessionContextWindowRequest{
		BotID:     "bot-1",
		SessionID: "sess-1",
		MaxTokens: 8000,
	})
	if err != nil {
		t.Fatalf("ComposeSessionContextWindow error: %v", err)
	}
	if len(untrimmed.Entries) != 2 {
		t.Fatalf("expected both messages as entries, got %d", len(untrimmed.Entries))
	}
	for _, entry := range untrimmed.Entries {
		if entry.Kind == "message" && entry.MessageID == "" {
			t.Fatalf("synthetic marker leaked into entries: %+v", entry)
		}
	}
	if untrimmed.Truncated {
		t.Fatal("markers must not count as truncation when nothing was dropped")
	}
}

func TestComposeSessionContextWindowRowCapReportsTruncation(t *testing.T) {
	t.Parallel()

	base := time.Now().UTC().Add(-2 * time.Hour)
	messages := make([]messagepkg.Message, 0, 1001)
	for i := range 1001 {
		messages = append(messages, composeWindowMessage(t, fmt.Sprintf("msg-%04d", i), "sess-1", "user", "ok", base.Add(time.Duration(i)*time.Second)))
	}
	msgs := &composeWindowMessageService{msgs: messages}
	svc := composeWindowService(msgs, session.Thread{ID: "sess-1", BotID: "bot-1"})

	result, err := svc.ComposeSessionContextWindow(context.Background(), tools.SessionContextWindowRequest{
		BotID:     "bot-1",
		SessionID: "sess-1",
		MaxTokens: 16000,
	})
	if err != nil {
		t.Fatalf("ComposeSessionContextWindow error: %v", err)
	}
	if !result.Truncated {
		t.Fatal("expected the row cap to be reported as truncation")
	}
	if len(result.Entries) != 1000 {
		t.Fatalf("expected the capped row count, got %d", len(result.Entries))
	}
	if result.Entries[len(result.Entries)-1].MessageID != "msg-1000" {
		t.Fatalf("expected the newest rows to survive the cap, got last=%+v", result.Entries[len(result.Entries)-1])
	}
}

type frontierCountingQueries struct {
	dbstore.Queries
	frontierCalls int
}

func (q *frontierCountingQueries) ListCompactionArtifactLineageBySession(context.Context, pgtype.UUID) ([]sqlc.BotHistoryMessageCompact, error) {
	q.frontierCalls++
	return nil, nil
}

func TestComposeSessionContextWindowFoldingOnlyForOverview(t *testing.T) {
	t.Parallel()

	const sessUUID = "33333333-3333-3333-3333-333333333333"
	base := time.Now().UTC().Add(-time.Hour)
	anchor := composeWindowMessage(t, "msg-a", sessUUID, "user", "anchor detail", base)
	queries := &frontierCountingQueries{}
	msgs := &composeWindowMessageService{
		msgs: []messagepkg.Message{anchor},
		byID: map[string]messagepkg.Message{"msg-a": anchor},
	}
	svc := &Service{
		messageService: msgs,
		sessionService: &composeWindowSessionService{thread: session.Thread{ID: sessUUID, BotID: "bot-1"}},
		queries:        queries,
	}

	if _, err := svc.ComposeSessionContextWindow(context.Background(), tools.SessionContextWindowRequest{
		BotID:           "bot-1",
		SessionID:       sessUUID,
		AnchorMessageID: "msg-a",
	}); err != nil {
		t.Fatalf("anchored compose error: %v", err)
	}
	if queries.frontierCalls != 0 {
		t.Fatalf("anchored windows must serve raw detail without summary folding, got %d frontier loads", queries.frontierCalls)
	}

	if _, err := svc.ComposeSessionContextWindow(context.Background(), tools.SessionContextWindowRequest{
		BotID:     "bot-1",
		SessionID: sessUUID,
	}); err != nil {
		t.Fatalf("overview compose error: %v", err)
	}
	if queries.frontierCalls != 1 {
		t.Fatalf("overview windows must fold summaries, got %d frontier loads", queries.frontierCalls)
	}
}

func TestSessionContextEntryTextHandlesPersistedShapes(t *testing.T) {
	t.Parallel()

	toolCallParts := historyRecord("msg-tc", ModelMessage{
		Role:    "assistant",
		Content: json.RawMessage(`[{"type":"tool-call","toolName":"exec"}]`),
	}, nil)
	toolResultParts := historyRecord("msg-tr", ModelMessage{
		Role:    "tool",
		Content: json.RawMessage(`[{"type":"tool-result","toolName":"exec"}]`),
	}, nil)
	bareToolRow := historyRecord("msg-bare", ModelMessage{
		Role:    "tool",
		Content: json.RawMessage(`""`),
	}, nil)
	summary := historyRecord("", ModelMessage{Role: "user", Content: newTextContent("<summary>we picked pgvector</summary>")}, func(r *historyfrag.HistoryRecord) {
		r.SourceKind = historyfrag.SourceCompactionLog
		r.Ref = contextfrag.ContextRef{Namespace: "compaction_log", ID: "compact-2"}
	})
	synthetic := historyRecord("", ModelMessage{Role: "system", Content: newTextContent("[Execution location] ...")}, func(r *historyfrag.HistoryRecord) {
		r.Synthetic = true
	})

	entries := sessionContextEntriesFromRecords([]historyfrag.HistoryRecord{toolCallParts, toolResultParts, bareToolRow, summary, synthetic})
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries (synthetic skipped), got %d: %+v", len(entries), entries)
	}
	if entries[0].Text != "[tool_call: exec]" {
		t.Fatalf("tool-call parts rendering = %q", entries[0].Text)
	}
	if entries[1].Text != "[tool_result: exec]" {
		t.Fatalf("tool-result parts rendering = %q", entries[1].Text)
	}
	if entries[2].Text != "[tool_result]" {
		t.Fatalf("bare tool row rendering = %q", entries[2].Text)
	}
	if entries[3].Kind != "summary" || entries[3].CompactID != "compact-2" {
		t.Fatalf("summary CompactID fallback via Ref.ID failed: %+v", entries[3])
	}
	if entries[3].Text != "<summary>we picked pgvector</summary>" {
		t.Fatalf("summary text must pass through unchanged, got %q", entries[3].Text)
	}
}

func TestSessionContextEntriesFromRecords(t *testing.T) {
	t.Parallel()

	summary := historyRecord("", ModelMessage{Role: "user", Content: newTextContent("Earlier: we chose pgvector.")}, func(r *historyfrag.HistoryRecord) {
		r.SourceKind = historyfrag.SourceCompactionLog
		r.Ref = contextfrag.ContextRef{Namespace: "compaction_log", ID: "compact-1"}
		r.CompactID = "compact-1"
		r.SessionID = "sess-1"
	})
	toolCall := historyRecord("msg-tc", ModelMessage{
		Role:      "assistant",
		ToolCalls: []turn.ToolCall{{Function: turn.ToolCallFunction{Name: "web_search"}}},
	}, func(r *historyfrag.HistoryRecord) {
		r.SessionID = "sess-1"
	})
	entries := sessionContextEntriesFromRecords([]historyfrag.HistoryRecord{summary, toolCall})
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Kind != "summary" || entries[0].CompactID != "compact-1" || entries[0].Text != "Earlier: we chose pgvector." {
		t.Fatalf("unexpected summary entry: %+v", entries[0])
	}
	if entries[0].MessageID != "" {
		t.Fatalf("summary entry must not carry a message id, got %q", entries[0].MessageID)
	}
	if entries[1].Kind != "message" || entries[1].Text != "[tool_call: web_search]" {
		t.Fatalf("unexpected tool-call entry: %+v", entries[1])
	}
}
