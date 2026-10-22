package application

import (
	"context"
	"testing"
	"time"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

// The retry cutoff names the EMPTY failed assistant row of the previous
// attempt, and cleanup passes drop exactly that row. Resolving the cutoff
// after the drops would always report context.history_cutoff_missing and make
// retries impossible, so the cutoff must resolve against the raw window.
func TestPrepareHistoryContextResolvesRetryCutoffNamingFailedAssistantRow(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	user := persistedHistoryMessage(t, "user-1", "user", "real question")
	user.CreatedAt = base
	failed := persistedHistoryMessage(t, "assistant-failed", "assistant", "")
	failed.Content = []byte("[]")
	failed.Metadata = map[string]any{messagepkg.HistoryErrorCodeMetadataKey: "model_request_failed"}
	failed.CreatedAt = base.Add(time.Second)
	later := persistedHistoryMessage(t, "user-2", "user", "retry input")
	later.CreatedAt = base.Add(2 * time.Second)

	messages := &anchoredHistoryMessageService{
		recordingMessageService: &recordingMessageService{},
		history:                 []messagepkg.Message{user, failed, later},
	}
	svc := &Service{messageService: messages}

	prepared, err := svc.prepareHistoryContext(context.Background(), ChatRequest{
		BotID:                        "bot",
		ChatID:                       "bot",
		ThreadID:                     "session",
		HistoryCutoffBeforeMessageID: "assistant-failed",
	}, historyfrag.ScopeFallback{ChatID: "bot"}, 100000)
	if err != nil {
		t.Fatalf("prepareHistoryContext() error = %v, want the cutoff to resolve against the raw window", err)
	}
	if len(prepared.records) != 1 || prepared.records[0].DBMessageID != "user-1" {
		t.Fatalf("records = %v, want only the message before the failed cutoff row", historyRecordIDs(prepared.records))
	}
}

// A cutoff that genuinely is not in the anchored window must still fail
// closed — resolving earlier only fixes cutoffs that the cleanup passes drop.
func TestPrepareHistoryContextMissingCutoffStillFailsClosed(t *testing.T) {
	t.Parallel()

	messages := &anchoredHistoryMessageService{
		recordingMessageService: &recordingMessageService{},
		history:                 []messagepkg.Message{persistedHistoryMessage(t, "user-1", "user", "hello")},
	}
	svc := &Service{messageService: messages}

	_, err := svc.prepareHistoryContext(context.Background(), ChatRequest{
		BotID:                        "bot",
		ChatID:                       "bot",
		ThreadID:                     "session",
		HistoryCutoffBeforeMessageID: "missing-cutoff",
	}, historyfrag.ScopeFallback{ChatID: "bot"}, 100000)
	if err == nil {
		t.Fatal("a cutoff outside the loaded window must fail closed")
	}
}
