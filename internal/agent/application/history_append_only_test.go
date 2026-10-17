package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func (s *persistedHistoryMessageService) ListActiveSinceBySessionWithinBytes(context.Context, string, time.Time, int64) ([]messagepkg.Message, error) {
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
