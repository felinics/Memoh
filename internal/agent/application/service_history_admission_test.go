package application

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

type turnResponseSourceMessageService struct {
	recordingMessageService
	full      []messagepkg.Message
	projected []messagepkg.Message
	sessionID string
	maxBytes  int64
}

func (s *turnResponseSourceMessageService) ListActiveSinceBySessionWithinBytes(context.Context, string, time.Time, int64) ([]messagepkg.Message, error) {
	return s.full, nil
}

func (s *turnResponseSourceMessageService) ListTurnResponseSourcesSinceBySessionWithinBytes(_ context.Context, sessionID string, _ time.Time, maxBytes int64) ([]messagepkg.Message, error) {
	s.sessionID, s.maxBytes = sessionID, maxBytes
	return s.projected, nil
}

func TestLoadTurnResponsesReadsProjectedSources(t *testing.T) {
	t.Parallel()

	source := &turnResponseSourceMessageService{
		full:      []messagepkg.Message{persistedHistoryMessage(t, "full", "assistant", "full row")},
		projected: []messagepkg.Message{persistedHistoryMessage(t, "projected", "assistant", "projected row")},
	}
	svc := &Service{messageService: source, logger: slog.New(slog.DiscardHandler)}
	entries := svc.loadTurnResponses(context.Background(), "session-1", 1000)

	if source.sessionID != "session-1" || source.maxBytes != 1000*contextfrag.EstimateBytesPerToken {
		t.Fatalf("projected load got session=%q maxBytes=%d", source.sessionID, source.maxBytes)
	}
	if len(entries) != 1 || entries[0].SourceMessageID != "projected" {
		t.Fatalf("turn responses = %#v, want the projected source only", entries)
	}
}

func TestPrepareHistoryContextDecodesUndecodedAdmissionMetadata(t *testing.T) {
	t.Parallel()

	lifecycle := map[string]any{"version": 2, "selection_decisions": []any{map[string]any{"id": "history:1", "decision": "kept"}}}
	failed := admissionHistoryMessage(t, "failed", sdk.AssistantMessage(""), map[string]any{
		messagepkg.HistoryErrorCodeMetadataKey:  "agent.response_timeout",
		contextfrag.MetadataContextLifecycleKey: lifecycle,
	})
	checkpoint := admissionHistoryMessage(t, "checkpoint", sdk.Message{
		Role:    sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{sdk.ReasoningPart{Text: "partial reasoning"}, sdk.TextPart{Text: "partial text"}},
	}, map[string]any{
		messagepkg.AgentStepInterruptedMetadataKey: true,
		contextfrag.MetadataContextLifecycleKey:    lifecycle,
	})
	svc := &Service{
		messageService: &memoryQueryMessageService{messages: []messagepkg.Message{
			persistedHistoryMessage(t, "question", "user", "question"), failed, checkpoint,
		}},
		logger: slog.New(slog.DiscardHandler),
	}

	prepared, err := svc.prepareHistoryContext(context.Background(), ChatRequest{BotID: "bot-1", ThreadID: "session-1"}, historyfrag.ScopeFallback{}, 100000)
	if err != nil {
		t.Fatalf("prepareHistoryContext failed: %v", err)
	}
	if len(prepared.messages) != 2 || len(prepared.records) != 2 {
		t.Fatalf("prepared %d messages, %d records; want the timeout marker dropped", len(prepared.messages), len(prepared.records))
	}
	if got := prepared.messages[1].TextContent(); !strings.Contains(got, messagepkg.AgentStepInterruptedReasoningPrefix+"partial reasoning") {
		t.Fatalf("interrupted checkpoint was not projected: %q", got)
	}
	record := prepared.records[1]
	if _, ok := record.Metadata[contextfrag.MetadataContextLifecycleKey]; ok {
		t.Fatal("history record kept the lifecycle audit")
	}
	decoded := checkpoint
	if err := json.Unmarshal(checkpoint.RawMetadata, &decoded.Metadata); err != nil {
		t.Fatal(err)
	}
	if want := historyfrag.DBMessageSourceHash(decoded).Value; record.Ref.ContentHash != want {
		t.Fatalf("undecoded row hash = %s, want the decoded row's %s", record.Ref.ContentHash, want)
	}
}

// admissionHistoryMessage mirrors the byte-budgeted loaders, which hand over
// metadata undecoded.
func admissionHistoryMessage(t *testing.T, id string, message sdk.Message, metadata map[string]any) messagepkg.Message {
	t.Helper()
	content, err := json.Marshal(sdkMessagesToModelMessages([]sdk.Message{message})[0])
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	return messagepkg.Message{ID: id, Role: string(message.Role), Content: content, RawMetadata: raw}
}
