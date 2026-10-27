package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/toolexec"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/messageconv"
)

type persistedHistoryMessageService struct {
	*recordingMessageService
	history []messagepkg.Message
}

func (s *persistedHistoryMessageService) ListActiveSinceBySessionWithinBytes(context.Context, string, time.Time, int64, bool) ([]messagepkg.Message, error) {
	return append([]messagepkg.Message(nil), s.history...), nil
}

type chatCompletionsMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	ReasoningContent *string         `json:"reasoning_content"`
	ToolCallID       string          `json:"tool_call_id"`
	ToolCalls        []struct {
		ID string `json:"id"`
	} `json:"tool_calls"`
}

func persistedToolTurn(t *testing.T, turn int, at time.Time) []messagepkg.Message {
	t.Helper()
	callID := fmt.Sprintf("call-%d", turn)
	turnMessages := messageconv.SDKMessagesToModelMessages([]sdk.Message{
		sdk.UserMessage(fmt.Sprintf("question %d", turn)),
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			sdk.ReasoningPart{Text: fmt.Sprintf("reasoning %d", turn), Format: sdk.ReasoningFormatOpenAIChat},
			sdk.ToolCallPart{ToolCallID: callID, ToolName: "read", Input: toolexec.ArgumentsFromValue(map[string]any{"path": fmt.Sprintf("file-%d", turn)})},
		}},
		sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: callID, ToolName: "read", Result: toolexec.OutputFromValue(fmt.Sprintf("payload %d", turn))}),
		sdk.AssistantMessage(fmt.Sprintf("answer %d", turn)),
	})
	out := make([]messagepkg.Message, 0, len(turnMessages))
	for i, message := range turnMessages {
		content, err := json.Marshal(message)
		if err != nil {
			t.Fatalf("marshal history message: %v", err)
		}
		out = append(out, messagepkg.Message{
			ID:        fmt.Sprintf("turn-%d-%d", turn, i),
			BotID:     lifecycleTestBotID,
			SessionID: lifecycleTestSessionID,
			Role:      message.Role,
			Content:   content,
			CreatedAt: at.Add(time.Duration(turn*10+i) * time.Second),
		})
	}
	return out
}

func persistedToolTurns(t *testing.T, turns int) []messagepkg.Message {
	t.Helper()
	at := time.Now().Add(-time.Hour)
	var history []messagepkg.Message
	for turn := 1; turn <= turns; turn++ {
		history = append(history, persistedToolTurn(t, turn, at)...)
	}
	return history
}

func chatWithPersistedHistory(t *testing.T, history []messagepkg.Message) []chatCompletionsMessage {
	t.Helper()
	fixture := newDirectLifecycleFixture(t, directLifecycleModelSuccess)
	fixture.service.messageService = &persistedHistoryMessageService{
		recordingMessageService: fixture.messages,
		history:                 history,
	}
	if _, err := fixture.service.Chat(context.Background(), ChatRequest{
		BotID:                lifecycleTestBotID,
		ChatID:               lifecycleTestBotID,
		ThreadID:             lifecycleTestSessionID,
		Query:                directLifecyclePrompt,
		UserMessagePersisted: true,
	}); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	for _, body := range fixture.requests.all() {
		if !bytes.Contains(body, []byte(directLifecyclePrompt)) {
			continue
		}
		var request struct {
			Messages []chatCompletionsMessage `json:"messages"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		return request.Messages
	}
	t.Fatal("provider never received the chat request")
	return nil
}

func historyStart(t *testing.T, messages []chatCompletionsMessage) int {
	t.Helper()
	for i, message := range messages {
		if message.Role == "user" && strings.Contains(string(message.Content), "question 1") {
			return i
		}
	}
	t.Fatalf("persisted history missing from provider request: %#v", messages)
	return -1
}

func TestChatReplaysEarlierTurnToolExchangesAndReasoningVerbatim(t *testing.T) {
	const turns = 3
	messages := chatWithPersistedHistory(t, persistedToolTurns(t, turns))

	for turn := 1; turn <= turns; turn++ {
		callID := fmt.Sprintf("call-%d", turn)
		var call, result, reasoning bool
		for _, message := range messages {
			for _, toolCall := range message.ToolCalls {
				call = call || toolCall.ID == callID
			}
			if message.Role == "tool" && message.ToolCallID == callID && strings.Contains(string(message.Content), fmt.Sprintf("payload %d", turn)) {
				result = true
			}
			if message.ReasoningContent != nil && *message.ReasoningContent == fmt.Sprintf("reasoning %d", turn) {
				reasoning = true
			}
		}
		if !call || !result || !reasoning {
			t.Fatalf("turn %d replay: tool call=%v tool result=%v reasoning=%v; request messages %#v", turn, call, result, reasoning, messages)
		}
	}
}

func interruptedReasoningTurn(t *testing.T, at time.Time) []messagepkg.Message {
	t.Helper()
	turnMessages := messageconv.SDKMessagesToModelMessages([]sdk.Message{
		sdk.UserMessage("question 1"),
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			sdk.ReasoningPart{Text: "half a thought", Format: sdk.ReasoningFormatOpenAIChat},
		}},
	})
	out := make([]messagepkg.Message, 0, len(turnMessages))
	for i, message := range turnMessages {
		content, err := json.Marshal(message)
		if err != nil {
			t.Fatalf("marshal history message: %v", err)
		}
		row := messagepkg.Message{
			ID:        fmt.Sprintf("interrupted-%d", i),
			BotID:     lifecycleTestBotID,
			SessionID: lifecycleTestSessionID,
			Role:      message.Role,
			Content:   content,
			CreatedAt: at.Add(time.Duration(i) * time.Second),
		}
		if message.Role == "assistant" {
			row.Metadata = map[string]any{messagepkg.AgentStepInterruptedMetadataKey: true}
		}
		out = append(out, row)
	}
	return out
}

func TestChatRendersInterruptedCheckpointTheSameOnEveryLaterTurn(t *testing.T) {
	at := time.Now().Add(-2 * time.Hour)
	live := interruptedReasoningTurn(t, at)
	superseded := append(append([]messagepkg.Message(nil), live...), persistedToolTurn(t, 2, time.Now().Add(-time.Hour))...)

	before := chatWithPersistedHistory(t, live)
	after := chatWithPersistedHistory(t, superseded)

	beforeStart, afterStart := historyStart(t, before), historyStart(t, after)
	if len(before) < beforeStart+2 || len(after) < afterStart+2 {
		t.Fatalf("interrupted turn missing: before=%d after=%d", len(before), len(after))
	}
	checkpoint := before[beforeStart+1]
	if checkpoint.Role != "assistant" || !strings.Contains(string(checkpoint.Content), "half a thought") || checkpoint.ReasoningContent != nil {
		t.Fatalf("interrupted checkpoint must replay as visible text, got %+v content=%s", checkpoint, checkpoint.Content)
	}
	previous, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatalf("marshal before: %v", err)
	}
	next, err := json.Marshal(after[afterStart+1])
	if err != nil {
		t.Fatalf("marshal after: %v", err)
	}
	if !bytes.Equal(previous, next) {
		t.Fatalf("interrupted checkpoint rewritten once superseded:\nbefore %s\nafter  %s", previous, next)
	}
}

type foldingHistoryMessageService struct {
	*recordingMessageService
	folded, full []messagepkg.Message
	folds        []bool
}

func (s *foldingHistoryMessageService) ListActiveSinceBySessionWithinBytes(_ context.Context, _ string, _ time.Time, _ int64, fold bool) ([]messagepkg.Message, error) {
	s.folds = append(s.folds, fold)
	if fold {
		return s.folded, nil
	}
	return s.full, nil
}

// A folded window loads one row per compact group, so a group whose summary
// does not render in the request would lose the rows the window left out.
// The history then loads again without folding and renders them raw.
func TestPrepareHistoryContextReloadsWhenFoldedSummaryDoesNotRender(t *testing.T) {
	t.Parallel()
	const compactID = "44444444-4444-4444-8444-444444444444"
	row := func(id, role, text, compact string, summarized bool) messagepkg.Message {
		return messagepkg.Message{
			ID: id, BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID, Role: role,
			Content:   json.RawMessage(fmt.Sprintf(`{"role":%q,"content":%q}`, role, text)),
			CompactID: compact, Summarized: summarized, CreatedAt: time.Unix(1, 0),
		}
	}
	messages := &foldingHistoryMessageService{
		recordingMessageService: &recordingMessageService{},
		folded:                  []messagepkg.Message{row("input", "user", "input", "", false), row("step-2", "assistant", "step 2", compactID, true)},
		full:                    []messagepkg.Message{row("input", "user", "input", "", false), row("step-1", "assistant", "step 1", compactID, false), row("step-2", "assistant", "step 2", compactID, false)},
	}
	s := &Service{messageService: messages, queries: &recordingCompactionLogQueries{}}
	req := ChatRequest{BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID}
	prepared, err := s.prepareHistoryContext(context.Background(), req, historyScopeFallbackFromChatRequest(req), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := recordTexts(prepared.records), []string{"input", "step 1", "step 2"}; !slices.Equal(messages.folds, []bool{true, false}) || !slices.Equal(got, want) {
		t.Fatalf("folds=%v records=%v, want a folded load, then an unfolded one rendering %v", messages.folds, got, want)
	}
}
