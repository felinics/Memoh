package application

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/timeline"
)

type originHistoryMessageService struct {
	memoryQueryMessageService
	origin messagepkg.HistoryOrigin
	err    error
}

func (s *originHistoryMessageService) HistoryOriginBySession(context.Context, string) (messagepkg.HistoryOrigin, error) {
	return s.origin, s.err
}

// A history whose origin left messages out opens with the notice of what it
// left out, a system message that renders no source; one with nothing left
// out, or nothing to open, has none.
func TestPrepareHistoryContextOpensWithTheHistoryNotice(t *testing.T) {
	t.Parallel()

	origin := messagepkg.HistoryOrigin{
		At:           time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC),
		OmittedCount: 7,
		OmittedFirst: time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC),
		OmittedLast:  time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC),
	}
	rows := []messagepkg.Message{
		persistedHistoryMessage(t, "question", "user", "question"),
		persistedHistoryMessage(t, "answer", "assistant", "answer"),
	}
	prepare := func(service *originHistoryMessageService) (preparedHistoryContext, error) {
		svc := &Service{messageService: service, logger: slog.New(slog.DiscardHandler)}
		return svc.prepareHistoryContext(context.Background(), ChatRequest{BotID: "bot-1", ThreadID: "session-1"}, historyfrag.ScopeFallback{}, 100000)
	}

	prepared, err := prepare(&originHistoryMessageService{memoryQueryMessageService: memoryQueryMessageService{messages: rows}, origin: origin})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.messages) != 3 || len(prepared.records) != 3 {
		t.Fatalf("prepared %d messages, %d records; want the notice and the two rows", len(prepared.messages), len(prepared.records))
	}
	notice := prepared.records[0]
	if notice.ModelMessage.Role != "system" || notice.ModelMessage.TextContent() != timeline.HistoryNotice(origin) || !notice.Synthetic || notice.DBMessageID != "" {
		t.Fatalf("history opens with %+v, want the synthetic notice", notice)
	}
	if prepared.records[1].DBMessageID != "question" || prepared.records[2].DBMessageID != "answer" {
		t.Fatalf("the rows follow as %q, %q", prepared.records[1].DBMessageID, prepared.records[2].DBMessageID)
	}

	for name, service := range map[string]*originHistoryMessageService{
		"no origin":       {memoryQueryMessageService: memoryQueryMessageService{messages: rows}},
		"nothing omitted": {memoryQueryMessageService: memoryQueryMessageService{messages: rows}, origin: messagepkg.HistoryOrigin{At: origin.At}},
		"no history":      {origin: origin},
	} {
		prepared, err := prepare(service)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, record := range prepared.records {
			if record.ModelMessage.Role == "system" {
				t.Fatalf("%s: history carries a notice %q", name, record.ModelMessage.TextContent())
			}
		}
	}

	// History that cannot say where it starts is not loaded at all.
	if _, err := prepare(&originHistoryMessageService{memoryQueryMessageService: memoryQueryMessageService{messages: rows}, err: errors.New("origin unavailable")}); err == nil {
		t.Fatal("history loaded without its origin")
	}
}
