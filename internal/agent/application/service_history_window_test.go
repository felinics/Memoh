package application

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbstore "github.com/felinics/memoh/internal/db/store"
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
}

// Embedded nil interfaces deliberately panic on any storage read. Missing
// identity must not cause a bot-wide read or infer a session from source rows.
func TestMissingSessionNeverLoadsHistoryOrSummaries(t *testing.T) {
	for _, sessionID := range []string{"", "  "} {
		svc := &Service{messageService: &struct{ messagepkg.Service }{}, queries: &struct{ dbstore.Queries }{}}
		ctx := context.Background()
		prepared, err := svc.prepareHistoryContext(ctx, ChatRequest{BotID: "bot", ThreadID: sessionID}, historyfrag.ScopeFallback{ChatID: "bot"}, 1000)
		if err != nil || len(prepared.records) != 0 || len(prepared.messages) != 0 {
			t.Fatalf("history=%+v err=%v", prepared, err)
		}
		records := []historyfrag.HistoryRecord{{SessionID: "other-session", CompactID: "summary"}}
		got, err := svc.replaceCompactedMessages(ctx, sessionID, contextfrag.Scope{BotID: "bot", SessionID: "other-session"}, records, compactionArtifactBoundary{})
		if err != nil || len(got) != 0 {
			t.Fatalf("inferred history=%v err=%v", got, err)
		}
		trs, err := svc.loadTurnResponses(ctx, sessionID, 1000)
		if err != nil || len(trs) != 0 {
			t.Fatalf("TR=%v err=%v", trs, err)
		}
		artifacts, err := svc.loadTimelineArtifacts(ctx, "bot", sessionID)
		if err != nil || len(artifacts) != 0 {
			t.Fatalf("artifacts=%v err=%v", artifacts, err)
		}
		query := svc.buildMemoryQuery(ctx, ChatRequest{BotID: "bot", ThreadID: sessionID, Query: "current question"})
		if query.Query != "current question" || query.RecentMessages != 0 {
			t.Fatalf("memory query=%+v", query)
		}
	}
}

// Failed assistant rows can be the retry cutoff. Resolve that boundary before
// filtering empty error rows, or a valid retry silently loses its prior history.
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
	svc := &Service{messageService: &ageFilteredHistory{rows: []messagepkg.Message{prior, earlierFailure, request, cutoff, later}}, logger: slog.New(slog.DiscardHandler)}
	prepared, err := svc.prepareHistoryContext(t.Context(), ChatRequest{BotID: "bot", ThreadID: "thread", HistoryCutoffBeforeMessageID: cutoff.ID}, historyfrag.ScopeFallback{}, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.records) != 2 || prepared.records[0].DBMessageID != prior.ID || prepared.records[1].DBMessageID != request.ID {
		t.Fatalf("retry history lost the prefix or crossed its boundary: %+v", prepared.records)
	}
}
