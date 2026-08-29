package application

import (
	"context"
	"strings"
	"testing"
	"time"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type memoryQueryMessageService struct {
	recordingMessageService
	messages []messagepkg.Message
}

func (s *memoryQueryMessageService) ListActiveSinceBySession(context.Context, string, time.Time) ([]messagepkg.Message, error) {
	return append([]messagepkg.Message(nil), s.messages...), nil
}

func (s *memoryQueryMessageService) ListActiveSinceBySessionWithinBytes(context.Context, string, time.Time, int64) ([]messagepkg.Message, error) {
	return append([]messagepkg.Message(nil), s.messages...), nil
}

func (s *memoryQueryMessageService) ListActiveSinceWithinBytes(context.Context, string, time.Time, int64) ([]messagepkg.Message, error) {
	return append([]messagepkg.Message(nil), s.messages...), nil
}

func (*memoryQueryMessageService) MeasureActiveBySession(context.Context, string, time.Time) (messagepkg.ActiveMessagesMeasure, error) {
	return messagepkg.ActiveMessagesMeasure{}, nil
}

func TestMemoryQueryBuilderCombinesRecentUserMessages(t *testing.T) {
	t.Parallel()

	builder := memoryQueryBuilder{MaxRecentMessages: 2, MaxBytes: 2000, MaxLines: 20, MaxMessageRunes: 200}
	query := builder.Build(ChatRequest{Query: "What should I pack?"}, []historyfrag.HistoryRecord{
		{ModelMessage: ModelMessage{Role: "user", Content: newTextContent("I am visiting Berlin next week")}},
		{ModelMessage: ModelMessage{Role: "assistant", Content: newTextContent("Sounds fun.")}},
		{ModelMessage: ModelMessage{Role: "user", Content: newTextContent("I prefer tea over coffee")}},
		{ModelMessage: ModelMessage{Role: "user", Content: newTextContent("What should I pack?")}},
	})

	if query.Source != "current_query_with_history" {
		t.Fatalf("Source = %q, want current_query_with_history", query.Source)
	}
	if query.RecentMessages != 2 {
		t.Fatalf("RecentMessages = %d, want 2", query.RecentMessages)
	}
	for _, want := range []string{"Current user request:", "What should I pack?", "I am visiting Berlin", "I prefer tea"} {
		if !strings.Contains(query.Query, want) {
			t.Fatalf("query missing %q: %s", want, query.Query)
		}
	}
	if strings.Count(query.Query, "What should I pack?") != 1 {
		t.Fatalf("expected current query to be deduplicated, got: %s", query.Query)
	}
}

func TestMemoryQueryBuilderTruncates(t *testing.T) {
	t.Parallel()

	builder := memoryQueryBuilder{MaxRecentMessages: 1, MaxBytes: 120, MaxLines: 6, MaxMessageRunes: 500}
	query := builder.Build(ChatRequest{Query: strings.Repeat("current ", 30)}, []historyfrag.HistoryRecord{
		{ModelMessage: ModelMessage{Role: "user", Content: newTextContent(strings.Repeat("history ", 30))}},
	})

	if !query.Truncated {
		t.Fatal("expected query to be marked truncated")
	}
	if len(query.Query) > 120 {
		t.Fatalf("query length = %d, want <= 120: %q", len(query.Query), query.Query)
	}
}

func TestMemoryQueryBuilderSkipsEmptyVisibleQuery(t *testing.T) {
	t.Parallel()

	query := defaultMemoryQueryBuilder().Build(ChatRequest{Query: "   "}, []historyfrag.HistoryRecord{
		{ModelMessage: ModelMessage{Role: "user", Content: newTextContent("history")}},
	})
	if query.Query != "" {
		t.Fatalf("expected empty query, got %+v", query)
	}
}

func TestBuildMemoryQueryRespectsRetryCutoffForRawAndCompactedHistory(t *testing.T) {
	t.Parallel()

	botID := "00000000-0000-0000-0000-00000000b715"
	sessionID := "00000000-0000-0000-0000-00000000e715"
	compactID := "00000000-0000-0000-0000-00000000c715"
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	prior := persistedHistoryMessage(t, "prior-user", "user", "prior visible context")
	prior.BotID = botID
	prior.SessionID = sessionID
	prior.CreatedAt = base.Add(10 * time.Minute)
	cutoff := persistedHistoryMessage(t, "cutoff-user", "user", "edited-away request")
	cutoff.BotID = botID
	cutoff.SessionID = sessionID
	cutoff.CreatedAt = base.Add(20 * time.Minute)

	resolver := &Service{
		messageService: &memoryQueryMessageService{messages: []messagepkg.Message{prior, cutoff}},
		queries: &recordingCompactionLogQueries{logs: []sqlc.BotHistoryMessageCompact{{
			ID:            mustPGUUID(t, compactID),
			BotID:         mustPGUUID(t, botID),
			SessionID:     mustPGUUID(t, sessionID),
			Status:        "ok",
			Summary:       "edited-away compacted context",
			AnchorStartMs: base.Add(15 * time.Minute).UnixMilli(),
			AnchorEndMs:   cutoff.CreatedAt.UnixMilli(),
		}}},
	}
	query := resolver.buildMemoryQuery(context.Background(), ChatRequest{
		BotID:                        botID,
		ThreadID:                     sessionID,
		Query:                        "replacement request",
		HistoryCutoffBeforeMessageID: cutoff.ID,
	})

	if !strings.Contains(query.Query, "prior visible context") {
		t.Fatalf("memory query lost pre-cutoff context: %q", query.Query)
	}
	for _, excluded := range []string{"edited-away request", "edited-away compacted context"} {
		if strings.Contains(query.Query, excluded) {
			t.Fatalf("memory query resurrected %q across retry cutoff: %q", excluded, query.Query)
		}
	}
}
