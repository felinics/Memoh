package application

import (
	"context"
	"strings"
	"testing"
	"time"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// The raw history after the session's checkpoint, whatever its age: a
// session without one has all of it.
type idleSessionHistory struct {
	recordingMessageService
	rows      []messagepkg.Message
	truncated bool
	after     messagepkg.HistoryPosition
	budget    int64
}

func (h *idleSessionHistory) ListHistoryWithinBytes(_ context.Context, _ string, window messagepkg.HistoryWindow) (messagepkg.HistoryLoad, error) {
	h.after, h.budget = window.After, window.MaxBytes
	return messagepkg.HistoryLoad{Messages: append([]messagepkg.Message(nil), h.rows...), Truncated: h.truncated}, nil
}

func (h *idleSessionHistory) ListTurnResponseSourcesWithinBytes(_ context.Context, _ string, after messagepkg.HistoryPosition, budget int64) (messagepkg.HistoryLoad, error) {
	h.after, h.budget = after, budget
	return messagepkg.HistoryLoad{Messages: append([]messagepkg.Message(nil), h.rows...)}, nil
}

func TestSessionHistorySurvivesIdleDays(t *testing.T) {
	old := time.Now().Add(-72 * time.Hour)
	user := persistedHistoryMessage(t, "old-user", "user", "project codeword BLUE_WHALE")
	answer := persistedHistoryMessage(t, "old-answer", "assistant", "confirmed BLUE_WHALE")
	user.CreatedAt, answer.CreatedAt = old, old.Add(time.Second)
	source := &idleSessionHistory{rows: []messagepkg.Message{user, answer}}
	svc := checkpointTestService(source, &historyCheckpointQueries{})
	req := ChatRequest{BotID: "bot", ThreadID: lifecycleTestSessionID, Query: "what was the codeword?"}
	prepared, err := prepareHistoryFor(context.Background(), svc, req, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.messages) != 2 || !strings.Contains(prepared.messages[0].TextContent(), "BLUE_WHALE") {
		t.Fatalf("idle session lost raw history: %#v", prepared.messages)
	}
	if source.after != (messagepkg.HistoryPosition{}) || source.budget != svc.historyLoadMaxBytes(1000) {
		t.Fatalf("record path read after %+v within %d bytes, want the whole history within its byte budget", source.after, source.budget)
	}
	trs, _, err := svc.loadTurnResponses(context.Background(), req.ThreadID, messagepkg.HistoryPosition{}, 1000)
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
}

// Embedded nil interfaces deliberately panic on any storage read. A request
// without a session has no history, and never widens the read to the bot.
func TestMissingSessionNeverLoadsHistory(t *testing.T) {
	for _, sessionID := range []string{"", "  "} {
		svc := checkpointTestService(&struct{ messagepkg.Service }{}, &struct{ dbstore.Queries }{})
		ctx := context.Background()
		prepared, err := prepareHistoryFor(ctx, svc, ChatRequest{BotID: "bot", ThreadID: sessionID}, 1000)
		if err != nil || len(prepared.records) != 0 || len(prepared.messages) != 0 {
			t.Fatalf("history=%+v err=%v", prepared, err)
		}
		trs, _, err := svc.loadTurnResponses(ctx, sessionID, messagepkg.HistoryPosition{}, 1000)
		if err != nil || len(trs) != 0 {
			t.Fatalf("TR=%v err=%v", trs, err)
		}
		query := svc.buildMemoryQuery(ctx, ChatRequest{BotID: "bot", ThreadID: sessionID, Query: "current question"})
		if query.Query != "current question" || query.RecentMessages != 0 {
			t.Fatalf("memory query=%+v", query)
		}
	}
}

// A failed turn's empty assistant row can be the retry cutoff. The cut is
// placed at that row before the load, so the cleanup that drops empty error
// rows afterwards can neither lose the prior history nor let later rows in.
func TestRetryEmptyFailureKeepsPriorHistoryAndExcludesLaterRows(t *testing.T) {
	old := time.Now().Add(-72 * time.Hour)
	prior := persistedHistoryMessage(t, "prior", "user", "retain PRIOR_CODEWORD")
	prior.CreatedAt = old
	earlierFailure := persistedHistoryMessage(t, "earlier-failure", "assistant", "")
	earlierFailure.CreatedAt = old.Add(time.Second)
	earlierFailure.Metadata = map[string]any{messagepkg.HistoryErrorCodeMetadataKey: "context.budget_unsatisfied"}
	request := persistedHistoryMessage(t, "request", "user", "retry this question")
	request.CreatedAt = time.Now().Add(-time.Minute)
	cutoff := persistedHistoryMessage(t, "cutoff", "assistant", "")
	cutoff.CreatedAt = request.CreatedAt.Add(time.Second)
	cutoff.Metadata = map[string]any{messagepkg.HistoryErrorCodeMetadataKey: "context.budget_unsatisfied"}
	later := persistedHistoryMessage(t, "later", "user", "exclude FUTURE_CODEWORD")
	later.CreatedAt = time.Now()
	svc := checkpointTestService(&memoryQueryMessageService{messages: []messagepkg.Message{prior, earlierFailure, request, cutoff, later}}, &historyCheckpointQueries{})
	prepared, err := prepareHistoryFor(t.Context(), svc, ChatRequest{BotID: "bot", ThreadID: lifecycleTestSessionID, HistoryCutoffBeforeMessageID: cutoff.ID}, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.records) != 2 || prepared.records[0].DBMessageID != prior.ID || prepared.records[1].DBMessageID != request.ID {
		t.Fatalf("retry history lost the prefix or crossed its boundary: %+v", prepared.records)
	}
}
