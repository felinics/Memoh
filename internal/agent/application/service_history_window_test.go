package application

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

// Apply the actual time predicate in the fake: these tests fail when a caller
// reintroduces an age cutoff, rather than merely asserting an argument value.
type ageFilteredHistory struct {
	recordingMessageService
	rows   []messagepkg.Message
	budget int64
	since  time.Time
}

func (h *ageFilteredHistory) selectSince(since time.Time, budget int64) []messagepkg.Message {
	h.since, h.budget = since, budget
	var rows []messagepkg.Message
	for _, row := range h.rows {
		if !row.CreatedAt.Before(since) {
			rows = append(rows, row)
		}
	}
	return rows
}

func (h *ageFilteredHistory) ListActiveSinceBySessionWithinBytes(_ context.Context, _ string, since time.Time, budget int64, _ bool) ([]messagepkg.Message, error) {
	return h.selectSince(since, budget), nil
}

func (h *ageFilteredHistory) ListActiveSinceWithinBytes(_ context.Context, _ string, since time.Time, budget int64) ([]messagepkg.Message, error) {
	return h.selectSince(since, budget), nil
}

func (h *ageFilteredHistory) ListTurnResponseSourcesSinceBySessionWithinBytes(_ context.Context, _ string, since time.Time, budget int64) ([]messagepkg.Message, error) {
	return h.selectSince(since, budget), nil
}

func TestSessionHistorySurvivesIdleDays(t *testing.T) {
	old := time.Now().Add(-72 * time.Hour)
	user := persistedHistoryMessage(t, "old-user", "user", "project codeword BLUE_WHALE")
	answer := persistedHistoryMessage(t, "old-answer", "assistant", "confirmed BLUE_WHALE")
	user.CreatedAt, answer.CreatedAt = old, old.Add(time.Second)
	source := &ageFilteredHistory{rows: []messagepkg.Message{user, answer}}
	svc := &Service{messageService: source, logger: slog.New(slog.DiscardHandler)}
	req := ChatRequest{BotID: "bot", ThreadID: "thread", Query: "what was the codeword?"}
	prepared, err := svc.prepareHistoryContext(context.Background(), req, historyfrag.ScopeFallback{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.messages) != 2 || !strings.Contains(prepared.messages[0].TextContent(), "BLUE_WHALE") {
		t.Fatalf("idle session lost raw history: %#v", prepared.messages)
	}
	if source.budget != svc.historyLoadMaxBytes(1000) {
		t.Fatal("record path lost its byte budget")
	}
	trs, err := svc.loadTurnResponses(context.Background(), req.ThreadID, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(trs) != 1 || trs[0].SourceMessageID != answer.ID {
		t.Fatalf("pipeline lost old answer: %#v", trs)
	}
	if source.budget != svc.historyLoadMaxBytes(1000) {
		t.Fatal("pipeline lost its byte budget")
	}
	memory := svc.buildMemoryQuery(context.Background(), req)
	if !strings.Contains(memory.Query, "BLUE_WHALE") {
		t.Fatalf("memory query lost old context: %s", memory.Query)
	}
	// A request without a thread must not suddenly restore the whole bot's
	// cross-session backlog. Keep that legacy fallback's original window.
	records, err := svc.loadHistoryRecords(context.Background(), historyfrag.ScopeFallback{ChatID: "bot"}, "", 1000, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatal("sessionless fallback broadened to old cross-session history")
	}
}
