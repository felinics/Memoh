package application

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/chat/checkpoint"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

// memoryQueryMessageService serves a session's history from memory: each
// message is a turn of its own, in slice order.
type memoryQueryMessageService struct {
	recordingMessageService
	messages []messagepkg.Message
	windows  []messagepkg.HistoryWindow
}

// historyTestPosition is where memoryQueryMessageService files its i-th
// message.
func historyTestPosition(i int) messagepkg.HistoryPosition {
	return messagepkg.HistoryPosition{Turn: int64(i + 1), Seq: 1}
}

func (s *memoryQueryMessageService) GetByIDBySession(_ context.Context, _ string, messageID string) (messagepkg.Message, error) {
	for _, message := range s.messages {
		if message.ID == messageID {
			return message, nil
		}
	}
	return messagepkg.Message{}, errors.New("message not found")
}

func (s *memoryQueryMessageService) HistoryPositionByID(_ context.Context, _ string, messageID string) (messagepkg.HistoryPosition, error) {
	for i, message := range s.messages {
		if message.ID == messageID {
			return historyTestPosition(i), nil
		}
	}
	return messagepkg.HistoryPosition{}, errors.New("message not found")
}

func (s *memoryQueryMessageService) ListActiveSinceBySession(context.Context, string, time.Time) ([]messagepkg.Message, error) {
	return append([]messagepkg.Message(nil), s.messages...), nil
}

func (s *memoryQueryMessageService) ListHistoryWithinBytes(_ context.Context, _ string, window messagepkg.HistoryWindow) (messagepkg.HistoryLoad, error) {
	s.windows = append(s.windows, window)
	var out []messagepkg.Message
	for i, message := range s.messages {
		position := historyTestPosition(i)
		if window.After.Before(position) && (window.Before == nil || position.Before(*window.Before)) {
			out = append(out, message)
		}
	}
	return messagepkg.HistoryLoad{Messages: out}, nil
}

func (s *memoryQueryMessageService) ListHistoryByIDs(_ context.Context, _ string, ids []string) ([]messagepkg.Message, error) {
	var out []messagepkg.Message
	for _, message := range s.messages {
		if slices.Contains(ids, message.ID) {
			out = append(out, message)
		}
	}
	return out, nil
}

func (s *memoryQueryMessageService) ListTurnResponseSourcesWithinBytes(_ context.Context, _ string, after messagepkg.HistoryPosition, _ int64) (messagepkg.HistoryLoad, error) {
	var out []messagepkg.Message
	for i, message := range s.messages {
		if after.Before(historyTestPosition(i)) {
			out = append(out, message)
		}
	}
	return messagepkg.HistoryLoad{Messages: out}, nil
}

func (s *memoryQueryMessageService) ListTurnResponseSourcesByIDs(ctx context.Context, sessionID string, ids []string) ([]messagepkg.Message, error) {
	return s.ListHistoryByIDs(ctx, sessionID, ids)
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

// A retry reads the history before its cutoff under the checkpoint its
// replacement keeps: neither the request it replaces nor a summary that
// folded it reaches the memory query.
func TestBuildMemoryQueryRespectsRetryCutoffForRawAndCompactedHistory(t *testing.T) {
	t.Parallel()

	botID := "00000000-0000-0000-0000-00000000b715"
	sessionID := "00000000-0000-0000-0000-00000000e715"
	turnID := "00000000-0000-0000-0000-00000000a715"
	prior := persistedHistoryMessage(t, "prior-user", "user", "prior visible context")
	cutoff := persistedHistoryMessage(t, "cutoff-user", "user", "edited-away request")
	queries := &historyCheckpointQueries{}
	queries.activate(historyCheckpointRow(t, "00000000-0000-0000-0000-00000000c716", historyTestPosition(1),
		checkpoint.Item{SummaryID: queries.summary(t, "00000000-0000-0000-0000-00000000c715", "edited-away compacted context")}))

	resolver := &Service{
		messageService: &memoryQueryMessageService{messages: []messagepkg.Message{prior, cutoff}},
		checkpoints:    checkpoint.NewStore(slog.New(slog.DiscardHandler), queries),
	}
	query := resolver.buildMemoryQuery(context.Background(), ChatRequest{
		BotID:                        botID,
		ThreadID:                     sessionID,
		Query:                        "replacement request",
		HistoryCutoffBeforeMessageID: cutoff.ID,
		TurnReplacement:              &messagepkg.TurnReplacement{OldTurnID: turnID},
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
