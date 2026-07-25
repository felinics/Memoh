package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/context/history"
	messagepkg "github.com/memohai/memoh/domains/agent/chat/message"
)

type requiredHistoryMessageService struct {
	recordingMessageService
	visible []messagepkg.Message
	byID    map[string]messagepkg.Message
}

func (s *requiredHistoryMessageService) ListVisibleFromBySession(context.Context, string, string) ([]messagepkg.Message, error) {
	return append([]messagepkg.Message(nil), s.visible...), nil
}

func (s *requiredHistoryMessageService) GetByIDBySession(_ context.Context, _ string, messageID string) (messagepkg.Message, error) {
	msg, ok := s.byID[messageID]
	if !ok {
		return messagepkg.Message{}, errors.New("message not found")
	}
	return msg, nil
}

func TestEnsureRequiredHistoryMessageMergesVisibleWindowInOrder(t *testing.T) {
	t.Parallel()

	requiredUser := persistedHistoryMessage(t, "user-1", "user", "retry me")
	toolCallAssistant := persistedHistoryMessage(t, "assistant-tool-1", "assistant", "calling tool")
	toolResult := persistedHistoryMessage(t, "tool-1", "tool", "tool result")
	finalAssistant := persistedHistoryMessage(t, "assistant-final-1", "assistant", "old answer")
	service := &requiredHistoryMessageService{
		visible: []messagepkg.Message{requiredUser, toolCallAssistant, toolResult, finalAssistant},
		byID:    map[string]messagepkg.Message{"user-1": requiredUser},
	}
	resolver := &Service{messageService: service}
	existing := []history.HistoryRecord{
		historyRecord("prior", agentdomain.ModelMessage{
			Role:    "assistant",
			Content: agentdomain.NewTextContent("prior context"),
		}, nil),
		historyRecord("assistant-tool-1", agentdomain.ModelMessage{Role: "assistant", Content: agentdomain.NewTextContent("calling tool")}, nil),
		historyRecord("tool-1", agentdomain.ModelMessage{Role: "tool", Content: agentdomain.NewTextContent("tool result")}, nil),
	}

	got, err := resolver.ensureRequiredHistoryMessage(context.Background(), existing, ChatRequest{
		ChatID:                       "bot-1",
		ThreadID:                     "session-1",
		RequiredHistoryMessageID:     "user-1",
		HistoryCutoffBeforeMessageID: "assistant-final-1",
	})
	if err != nil {
		t.Fatalf("ensureRequiredHistoryMessage() error = %v", err)
	}
	if gotIDs := historyRecordIDs(got); !equalStrings(gotIDs, []string{"prior", "user-1", "assistant-tool-1", "tool-1"}) {
		t.Fatalf("message ids = %v, want prior + ordered required window", gotIDs)
	}
	if !got[1].Required {
		t.Fatalf("required message was not marked")
	}
}

func TestFilterMessagesBeforeIDFailsClosedWhenCutoffIsMissing(t *testing.T) {
	t.Parallel()

	records := []history.HistoryRecord{
		historyRecord("later-user", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("later")}, nil),
	}
	if got := filterMessagesBeforeID(records, "missing-cutoff"); len(got) != 0 {
		t.Fatalf("missing cutoff retained history: %#v", got)
	}
}

func TestLoadCompactionArtifactBoundaryFetchesCutoffOutsideLoadedWindow(t *testing.T) {
	t.Parallel()

	cutoff := persistedHistoryMessage(t, "old-cutoff", "assistant", "old answer")
	cutoff.CreatedAt = time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	resolver := &Service{messageService: &requiredHistoryMessageService{
		byID: map[string]messagepkg.Message{cutoff.ID: cutoff},
	}}

	boundary := resolver.loadCompactionArtifactBoundary(context.Background(), nil, "session-1", cutoff.ID)
	if !boundary.hasCutoff || boundary.cutoffMs != cutoff.CreatedAt.UnixMilli() {
		t.Fatalf("boundary = %+v, want cutoff at %d", boundary, cutoff.CreatedAt.UnixMilli())
	}
}

func persistedHistoryMessage(t *testing.T, id string, role string, text string) messagepkg.Message {
	t.Helper()
	content, err := json.Marshal(agentdomain.ModelMessage{
		Role:    role,
		Content: agentdomain.NewTextContent(text),
	})
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	return messagepkg.Message{
		ID:      id,
		Role:    role,
		Content: content,
	}
}

func historyRecordIDs(messages []history.HistoryRecord) []string {
	out := make([]string, 0, len(messages))
	for _, msg := range messages {
		out = append(out, msg.DBMessageID)
	}
	return out
}

func equalStrings(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
