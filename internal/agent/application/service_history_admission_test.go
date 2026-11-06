package application

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

type turnResponseSourceMessageService struct {
	recordingMessageService
	full      []messagepkg.Message
	projected []messagepkg.Message
	kept      []messagepkg.Message
	sessionID string
	after     messagepkg.HistoryPosition
	maxBytes  int64
	truncated bool
	err       error
}

func (s *turnResponseSourceMessageService) ListHistoryWithinBytes(context.Context, string, messagepkg.HistoryWindow) (messagepkg.HistoryLoad, error) {
	return messagepkg.HistoryLoad{Messages: s.full}, nil
}

func (s *turnResponseSourceMessageService) ListTurnResponseSourcesWithinBytes(_ context.Context, sessionID string, after messagepkg.HistoryPosition, maxBytes int64) (messagepkg.HistoryLoad, error) {
	s.sessionID, s.after, s.maxBytes = sessionID, after, maxBytes
	return messagepkg.HistoryLoad{Messages: s.projected, Truncated: s.truncated}, s.err
}

func (s *turnResponseSourceMessageService) ListTurnResponseSourcesByIDs(_ context.Context, _ string, ids []string) ([]messagepkg.Message, error) {
	var kept []messagepkg.Message
	for _, message := range s.kept {
		if slices.Contains(ids, message.ID) {
			kept = append(kept, message)
		}
	}
	return kept, nil
}

func TestLoadTurnResponsesReadsProjectedSources(t *testing.T) {
	t.Parallel()

	source := &turnResponseSourceMessageService{
		full:      []messagepkg.Message{persistedHistoryMessage(t, "full", "assistant", "full row")},
		projected: []messagepkg.Message{persistedHistoryMessage(t, "projected", "assistant", "projected row")},
	}
	svc := &Service{messageService: source, logger: slog.New(slog.DiscardHandler)}
	after := messagepkg.HistoryPosition{Turn: 12, Seq: 3}
	entries, _, err := svc.loadTurnResponses(context.Background(), "session-1", after, 1000)
	if err != nil {
		t.Fatal(err)
	}

	if source.sessionID != "session-1" || source.after != after || source.maxBytes != 1000*contextfrag.EstimateBytesPerToken {
		t.Fatalf("projected load got session=%q after=%+v maxBytes=%d", source.sessionID, source.after, source.maxBytes)
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
		checkpoints: emptyHistoryCheckpoints(),
		messageService: &memoryQueryMessageService{messages: []messagepkg.Message{
			persistedHistoryMessage(t, "question", "user", "question"), failed, checkpoint,
		}},
		logger: slog.New(slog.DiscardHandler),
	}

	prepared, err := prepareHistoryFor(context.Background(), svc, ChatRequest{BotID: "bot-1", ThreadID: lifecycleTestSessionID}, 100000)
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
