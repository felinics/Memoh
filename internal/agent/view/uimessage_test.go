package view

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/agent/turn"
	messagepkg "github.com/memohai/memoh/internal/chat/message"
)

func convertTestMessagesToUITurns(messages []messagepkg.Message) []UITurn {
	turnID := "test-turn"
	for i := range messages {
		if explicit := strings.TrimSpace(messages[i].TurnID); explicit != "" {
			turnID = explicit
		} else {
			messages[i].TurnID = turnID
		}
	}
	return ConvertMessagesToUITurns(messages)
}

func TestConvertMessagesToUITurnsGroupsAssistantToolAndKeepsCurrentConversationDelivery(t *testing.T) {
	baseTime := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	messages := []messagepkg.Message{
		{
			ID:             "user-1",
			TurnID:         "turn-1",
			BotID:          "bot-1",
			SessionID:      "session-1",
			Role:           "user",
			DisplayContent: "hello",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role:    "user",
				Content: mustUIRawJSON(t, "hello"),
			}),
			CreatedAt: baseTime,
		},
		{
			ID:        "assistant-1",
			TurnID:    "turn-1",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "assistant",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role: "assistant",
				Content: mustUIRawJSON(t, []map[string]any{
					{"type": "reasoning", "text": "thinking"},
					{"type": "tool-call", "toolCallId": "call-1", "toolName": "read", "input": map[string]any{"path": "/tmp/a.txt"}},
					{"type": "tool-call", "toolCallId": "call-2", "toolName": "send", "input": map[string]any{"message": "hi"}},
				}),
			}),
			Assets: []messagepkg.MessageAsset{{
				ContentHash: "hash-1",
				Mime:        "image/png",
				StorageKey:  "media/hash-1",
				Name:        "image.png",
			}},
			CreatedAt: baseTime.Add(1 * time.Minute),
		},
		{
			ID:        "tool-1",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "tool",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role: "tool",
				Content: mustUIRawJSON(t, []map[string]any{
					{"type": "tool-result", "toolCallId": "call-1", "toolName": "read", "result": map[string]any{"structuredContent": map[string]any{"stdout": "hello"}}},
				}),
			}),
			CreatedAt: baseTime.Add(2 * time.Minute),
		},
		{
			ID:        "tool-2",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "tool",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role: "tool",
				Content: mustUIRawJSON(t, []map[string]any{
					{"type": "tool-result", "toolCallId": "call-2", "toolName": "send", "result": map[string]any{"delivered": "current_conversation"}},
				}),
			}),
			CreatedAt: baseTime.Add(3 * time.Minute),
		},
		{
			ID:        "assistant-2",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "assistant",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role:    "assistant",
				Content: mustUIRawJSON(t, []map[string]any{{"type": "text", "text": "done"}}),
			}),
			CreatedAt: baseTime.Add(4 * time.Minute),
		},
	}

	turns := convertTestMessagesToUITurns(messages)
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(turns))
	}

	userTurn := turns[0]
	if userTurn.Role != "user" || userTurn.Text != "hello" {
		t.Fatalf("unexpected user turn: %#v", userTurn)
	}
	if userTurn.TurnID != "turn-1" {
		t.Fatalf("user turn id = %q, want turn-1", userTurn.TurnID)
	}

	assistantTurn := turns[1]
	if assistantTurn.Role != "assistant" {
		t.Fatalf("expected assistant turn, got %#v", assistantTurn)
	}
	if assistantTurn.TurnID != "turn-1" {
		t.Fatalf("assistant turn id = %q, want turn-1", assistantTurn.TurnID)
	}
	if len(assistantTurn.Messages) != 5 {
		t.Fatalf("expected 5 assistant messages, got %d", len(assistantTurn.Messages))
	}

	if assistantTurn.Messages[0].Type != UIMessageReasoning || assistantTurn.Messages[0].Content != "thinking" {
		t.Fatalf("unexpected reasoning block: %#v", assistantTurn.Messages[0])
	}
	if assistantTurn.Messages[1].Type != UIMessageTool || assistantTurn.Messages[1].Name != "read" {
		t.Fatalf("unexpected tool block: %#v", assistantTurn.Messages[1])
	}
	if assistantTurn.Messages[1].Running == nil || *assistantTurn.Messages[1].Running {
		t.Fatalf("expected tool block to be completed: %#v", assistantTurn.Messages[1])
	}
	if assistantTurn.Messages[2].Type != UIMessageTool || assistantTurn.Messages[2].Name != "send" {
		t.Fatalf("expected current conversation delivery tool to be retained: %#v", assistantTurn.Messages[2])
	}
	if assistantTurn.Messages[2].Running == nil || *assistantTurn.Messages[2].Running {
		t.Fatalf("expected send tool block to be completed: %#v", assistantTurn.Messages[2])
	}
	if assistantTurn.Messages[3].Type != UIMessageAttachments || len(assistantTurn.Messages[3].Attachments) != 1 {
		t.Fatalf("unexpected attachment block: %#v", assistantTurn.Messages[3])
	}
	if assistantTurn.Messages[3].Attachments[0].Type != "image" || assistantTurn.Messages[3].Attachments[0].BotID != "bot-1" {
		t.Fatalf("unexpected attachment payload: %#v", assistantTurn.Messages[3].Attachments[0])
	}
	if assistantTurn.Messages[4].Type != UIMessageText || assistantTurn.Messages[4].Content != "done" {
		t.Fatalf("unexpected trailing text block: %#v", assistantTurn.Messages[4])
	}
}

// A "talk while acting" reply persists as several separate assistant messages
// that interleave plain text with tool calls. They are one logical reply to a
// single user message, so they must collapse into a single assistant turn (one
// ActionBar). The opening and interstitial remarks must not split the turn.
func TestConvertMessagesToUITurnsMergesTalkWhileActingIntoOneTurn(t *testing.T) {
	baseTime := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	messages := []messagepkg.Message{
		{
			ID:             "user-1",
			BotID:          "bot-1",
			SessionID:      "session-1",
			Role:           "user",
			DisplayContent: "open the page and summarize it",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role:    "user",
				Content: mustUIRawJSON(t, "open the page and summarize it"),
			}),
			CreatedAt: baseTime,
		},
		{
			ID:        "assistant-opening",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "assistant",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role:    "assistant",
				Content: mustUIRawJSON(t, []map[string]any{{"type": "text", "text": "Sure, let me open it."}}),
			}),
			CreatedAt: baseTime.Add(1 * time.Second),
		},
		{
			ID:        "assistant-tool-1",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "assistant",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role: "assistant",
				Content: mustUIRawJSON(t, []map[string]any{
					{"type": "tool-call", "toolCallId": "call-1", "toolName": "browser_action", "input": map[string]any{"action": "navigate"}},
				}),
			}),
			CreatedAt: baseTime.Add(2 * time.Second),
		},
		{
			ID:        "tool-1",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "tool",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role: "tool",
				Content: mustUIRawJSON(t, []map[string]any{
					{"type": "tool-result", "toolCallId": "call-1", "toolName": "browser_action", "result": map[string]any{"structuredContent": map[string]any{"ok": true}}},
				}),
			}),
			CreatedAt: baseTime.Add(3 * time.Second),
		},
		{
			ID:        "assistant-interstitial",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "assistant",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role:    "assistant",
				Content: mustUIRawJSON(t, []map[string]any{{"type": "text", "text": "Page loaded, reading content."}}),
			}),
			CreatedAt: baseTime.Add(4 * time.Second),
		},
		{
			ID:        "assistant-tool-2",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "assistant",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role: "assistant",
				Content: mustUIRawJSON(t, []map[string]any{
					{"type": "tool-call", "toolCallId": "call-2", "toolName": "browser_observe", "input": map[string]any{}},
				}),
			}),
			CreatedAt: baseTime.Add(5 * time.Second),
		},
		{
			ID:        "tool-2",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "tool",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role: "tool",
				Content: mustUIRawJSON(t, []map[string]any{
					{"type": "tool-result", "toolCallId": "call-2", "toolName": "browser_observe", "result": map[string]any{"structuredContent": map[string]any{"text": "content"}}},
				}),
			}),
			CreatedAt: baseTime.Add(6 * time.Second),
		},
		{
			ID:        "assistant-closing",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "assistant",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role:    "assistant",
				Content: mustUIRawJSON(t, []map[string]any{{"type": "text", "text": "Here is the summary."}}),
			}),
			CreatedAt: baseTime.Add(7 * time.Second),
		},
	}

	turns := convertTestMessagesToUITurns(messages)
	if len(turns) != 2 {
		t.Fatalf("talk-while-acting reply must collapse into one user + one assistant turn, got %d", len(turns))
	}
	if turns[0].Role != "user" || turns[0].Text != "open the page and summarize it" {
		t.Fatalf("unexpected user turn: %#v", turns[0])
	}

	assistantTurn := turns[1]
	if assistantTurn.Role != "assistant" {
		t.Fatalf("expected merged assistant turn, got %#v", assistantTurn)
	}
	if len(assistantTurn.Messages) != 5 {
		t.Fatalf("expected 5 ordered blocks (text, tool, text, tool, text), got %d: %#v", len(assistantTurn.Messages), assistantTurn.Messages)
	}
	if assistantTurn.Messages[0].Type != UIMessageText || assistantTurn.Messages[0].Content != "Sure, let me open it." {
		t.Fatalf("block 0 should be the opening remark: %#v", assistantTurn.Messages[0])
	}
	if assistantTurn.Messages[1].Type != UIMessageTool || assistantTurn.Messages[1].Name != "browser_action" {
		t.Fatalf("block 1 should be the first tool call: %#v", assistantTurn.Messages[1])
	}
	if assistantTurn.Messages[2].Type != UIMessageText || assistantTurn.Messages[2].Content != "Page loaded, reading content." {
		t.Fatalf("block 2 should be the interstitial remark: %#v", assistantTurn.Messages[2])
	}
	if assistantTurn.Messages[3].Type != UIMessageTool || assistantTurn.Messages[3].Name != "browser_observe" {
		t.Fatalf("block 3 should be the second tool call: %#v", assistantTurn.Messages[3])
	}
	if assistantTurn.Messages[4].Type != UIMessageText || assistantTurn.Messages[4].Content != "Here is the summary." {
		t.Fatalf("block 4 should be the closing remark: %#v", assistantTurn.Messages[4])
	}
	for i, block := range assistantTurn.Messages {
		if block.Type != UIMessageTool {
			continue
		}
		if block.Running == nil || *block.Running {
			t.Fatalf("tool block %d should be completed once its result is applied: %#v", i, block)
		}
	}
	if !assistantTurn.Timestamp.Equal(baseTime.Add(1 * time.Second)) {
		t.Fatalf("merged turn timestamp must anchor to the first assistant message, got %v", assistantTurn.Timestamp)
	}
	if assistantTurn.ID != "assistant-opening" {
		t.Fatalf("merged turn id must anchor to the first assistant message, got %q", assistantTurn.ID)
	}
}

// Computer Use / Browser Use feed screenshots back into the loop as image-only
// user messages (empty display text, inline base64, no stored asset). They are
// internal feedback, not new user input, so they must be skipped without ending
// the assistant turn — otherwise every screenshot splits one reply into another
// turn (another action bar), which was the computer-use action-bar storm.
func TestConvertMessagesToUITurnsKeepsScreenshotFeedbackInOneTurn(t *testing.T) {
	baseTime := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	screenshotUser := func(id string, at time.Time) messagepkg.Message {
		return messagepkg.Message{
			ID:        id,
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "user",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role:    "user",
				Content: mustUIRawJSON(t, []map[string]any{{"type": "image", "image": "data:image/png;base64,AAAA"}}),
			}),
			CreatedAt: at,
		}
	}
	messages := []messagepkg.Message{
		{
			ID:             "user-1",
			BotID:          "bot-1",
			SessionID:      "session-1",
			Role:           "user",
			DisplayContent: "open the browser and pick a frame",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role:    "user",
				Content: mustUIRawJSON(t, "open the browser and pick a frame"),
			}),
			CreatedAt: baseTime,
		},
		{
			ID:        "assistant-nav",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "assistant",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role: "assistant",
				Content: mustUIRawJSON(t, []map[string]any{
					{"type": "tool-call", "toolCallId": "call-1", "toolName": "browser_action", "input": map[string]any{"action": "navigate"}},
				}),
			}),
			CreatedAt: baseTime.Add(1 * time.Second),
		},
		{
			ID:        "tool-nav",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "tool",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role: "tool",
				Content: mustUIRawJSON(t, []map[string]any{
					{"type": "tool-result", "toolCallId": "call-1", "toolName": "browser_action", "result": map[string]any{"structuredContent": map[string]any{"ok": true}}},
				}),
			}),
			CreatedAt: baseTime.Add(2 * time.Second),
		},
		screenshotUser("shot-1", baseTime.Add(3*time.Second)),
		{
			ID:        "assistant-retry",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "assistant",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role: "assistant",
				Content: mustUIRawJSON(t, []map[string]any{
					{"type": "text", "text": "Still loading, let me wait."},
					{"type": "tool-call", "toolCallId": "call-2", "toolName": "browser_action", "input": map[string]any{"action": "wait"}},
				}),
			}),
			CreatedAt: baseTime.Add(4 * time.Second),
		},
		{
			ID:        "tool-wait",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "tool",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role: "tool",
				Content: mustUIRawJSON(t, []map[string]any{
					{"type": "tool-result", "toolCallId": "call-2", "toolName": "browser_action", "result": map[string]any{"structuredContent": map[string]any{"ok": true}}},
				}),
			}),
			CreatedAt: baseTime.Add(5 * time.Second),
		},
		screenshotUser("shot-2", baseTime.Add(6*time.Second)),
		{
			ID:        "assistant-done",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "assistant",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role:    "assistant",
				Content: mustUIRawJSON(t, []map[string]any{{"type": "text", "text": "Got the frame I like."}}),
			}),
			CreatedAt: baseTime.Add(7 * time.Second),
		},
	}

	turns := convertTestMessagesToUITurns(messages)
	if len(turns) != 2 {
		t.Fatalf("screenshot feedback must not split the reply: want user + assistant (2 turns), got %d", len(turns))
	}
	if turns[0].Role != "user" || turns[0].Text != "open the browser and pick a frame" {
		t.Fatalf("unexpected user turn: %#v", turns[0])
	}
	assistantTurn := turns[1]
	if assistantTurn.Role != "assistant" {
		t.Fatalf("expected single merged assistant turn, got %#v", assistantTurn)
	}
	if len(assistantTurn.Messages) != 4 {
		t.Fatalf("expected 4 blocks (tool, text, tool, text), got %d: %#v", len(assistantTurn.Messages), assistantTurn.Messages)
	}
	wantText := []string{"Still loading, let me wait.", "Got the frame I like."}
	gotText := []string{}
	toolBlocks := 0
	for _, b := range assistantTurn.Messages {
		switch b.Type {
		case UIMessageText:
			gotText = append(gotText, b.Content)
		case UIMessageTool:
			toolBlocks++
			if b.Running == nil || *b.Running {
				t.Fatalf("tool block should be completed: %#v", b)
			}
		}
	}
	if toolBlocks != 2 {
		t.Fatalf("expected 2 completed tool blocks, got %d", toolBlocks)
	}
	if !reflect.DeepEqual(gotText, wantText) {
		t.Fatalf("assistant text blocks = %#v, want %#v", gotText, wantText)
	}
}

func TestConvertMessagesToUITurnsGroupsOnlyByTurnID(t *testing.T) {
	messages := []messagepkg.Message{
		{
			ID:        "assistant-1",
			TurnID:    "turn-1",
			Role:      "assistant",
			Content:   mustUIMessageJSON(t, turn.ModelMessage{Role: "assistant", Content: mustUIRawJSON(t, "first")}),
			CreatedAt: time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC),
		},
		{
			ID:        "internal-user",
			TurnID:    "turn-2",
			Role:      "user",
			Content:   mustUIMessageJSON(t, turn.ModelMessage{Role: "user", Content: mustUIRawJSON(t, "")}),
			CreatedAt: time.Date(2026, 7, 28, 9, 0, 1, 0, time.UTC),
		},
		{
			ID:        "assistant-2",
			TurnID:    "turn-2",
			Role:      "assistant",
			Content:   mustUIMessageJSON(t, turn.ModelMessage{Role: "assistant", Content: mustUIRawJSON(t, "second")}),
			CreatedAt: time.Date(2026, 7, 28, 9, 0, 2, 0, time.UTC),
		},
		{
			ID:        "missing-turn",
			Role:      "assistant",
			Content:   mustUIMessageJSON(t, turn.ModelMessage{Role: "assistant", Content: mustUIRawJSON(t, "must not render")}),
			CreatedAt: time.Date(2026, 7, 28, 9, 0, 3, 0, time.UTC),
		},
	}

	turns := ConvertMessagesToUITurns(messages)
	if len(turns) != 2 {
		t.Fatalf("turn count = %d, want 2: %#v", len(turns), turns)
	}
	if turns[0].TurnID != "turn-1" || turns[1].TurnID != "turn-2" {
		t.Fatalf("turn ids = %q, %q; want turn-1, turn-2", turns[0].TurnID, turns[1].TurnID)
	}
	if turns[0].Messages[0].Content != "first" || turns[1].Messages[0].Content != "second" {
		t.Fatalf("assistant content crossed turn boundary: %#v", turns)
	}
}

func TestConvertMessagesToUITurnsKeepsUserInputMetadata(t *testing.T) {
	t.Parallel()

	messages := []messagepkg.Message{{
		ID:        "assistant-1",
		BotID:     "bot-1",
		SessionID: "session-1",
		Role:      "assistant",
		Content: mustUIMessageJSON(t, turn.ModelMessage{
			Role: "assistant",
			Content: mustUIRawJSON(t, []map[string]any{{
				"type":       "tool-call",
				"toolCallId": "call-ask",
				"toolName":   "ask_user",
				"input":      map[string]any{"questions": []any{map[string]any{"text": "Which plan?", "kind": "single_select"}}},
				"providerMetadata": map[string]any{
					"user_input": map[string]any{
						"user_input_id": "input-1",
						"short_id":      2,
						"status":        "submitted",
						"answers": []any{map[string]any{
							"question_id": "q1",
							"question":    "Which plan?",
							"selected":    []any{map[string]any{"id": "q1.o2", "label": "Plan B"}},
						}},
						"ui_payload": map[string]any{
							"version": 2,
							"questions": []any{map[string]any{
								"id":   "q1",
								"text": "Which plan?",
								"kind": "single_select",
								"options": []any{
									map[string]any{"id": "q1.o1", "label": "Plan A"},
									map[string]any{"id": "q1.o2", "label": "Plan B"},
								},
							}},
						},
					},
				},
			}}),
		}),
		CreatedAt: time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC),
	}}

	turns := convertTestMessagesToUITurns(messages)
	if len(turns) != 1 || len(turns[0].Messages) != 1 {
		t.Fatalf("unexpected turns: %#v", turns)
	}
	block := turns[0].Messages[0]
	if block.UserInput == nil || block.UserInput.UserInputID != "input-1" {
		t.Fatalf("persisted user_input metadata must survive UITurn conversion: %#v", block)
	}
	if len(block.UserInput.Questions) != 1 || block.UserInput.Questions[0].Kind != "single_select" {
		t.Fatalf("unexpected questions: %#v", block.UserInput.Questions)
	}
	if len(block.UserInput.Answers) != 1 || block.UserInput.Answers[0].Selected[0].Label != "Plan B" {
		t.Fatalf("unexpected answers: %#v", block.UserInput.Answers)
	}
}

func TestConvertMessagesToUITurnsStripsUserYAMLHeaderFallback(t *testing.T) {
	now := time.Now().UTC()
	turns := convertTestMessagesToUITurns([]messagepkg.Message{{
		ID:        "user-1",
		BotID:     "bot-1",
		SessionID: "session-1",
		Role:      "user",
		Content: mustUIMessageJSON(t, turn.ModelMessage{
			Role:    "user",
			Content: mustUIRawJSON(t, "---\nmessage-id: 1\nchannel: telegram\n---\nhello"),
		}),
		CreatedAt: now,
	}})

	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if turns[0].Text != "hello" {
		t.Fatalf("expected YAML header to be stripped, got %q", turns[0].Text)
	}
}

func TestConvertMessagesToUITurnsStripsUserXMLEnvelopeFallback(t *testing.T) {
	now := time.Now().UTC()
	turns := convertTestMessagesToUITurns([]messagepkg.Message{{
		ID:        "user-1",
		BotID:     "bot-1",
		SessionID: "session-1",
		Role:      "user",
		Content: mustUIMessageJSON(t, turn.ModelMessage{
			Role: "user",
			Content: mustUIRawJSON(t, `<message id="msg-image-only" sender="Test User (@test_user)" t="2026-05-08T19:08:58Z" channel="telegram" conversation="Test Group" type="group" target="test-group">
<attachment path="/data/media/test/test-image.webp"/>

</message>`),
		}),
		Assets: []messagepkg.MessageAsset{{
			ContentHash: "test-image-hash",
			Mime:        "image/webp",
			StorageKey:  "media/test/test-image.webp",
			Name:        "image.webp",
		}},
		CreatedAt: now,
	}})

	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if turns[0].Text != "" {
		t.Fatalf("expected XML envelope to be stripped, got %q", turns[0].Text)
	}
	if len(turns[0].Attachments) != 1 || turns[0].Attachments[0].Type != "image" {
		t.Fatalf("expected image attachment to remain, got %#v", turns[0].Attachments)
	}
}

// Reasoning that streams before the answer text must keep a smaller block ID
// than the text block: the frontend sorts blocks by ID, so an eagerly created
// text block would pin the answer above the thinking that preceded it.
func TestUIMessageStreamConverterKeepsReasoningBeforeText(t *testing.T) {
	converter := NewUIMessageStreamConverter()

	reasoning := converter.HandleEvent(UIMessageStreamEvent{Type: "reasoning_delta", Delta: "thinking first"})
	if len(reasoning) != 1 || reasoning[0].Type != UIMessageReasoning {
		t.Fatalf("reasoning messages = %#v", reasoning)
	}
	text := converter.HandleEvent(UIMessageStreamEvent{Type: "text_delta", Delta: "answer"})
	if len(text) != 1 || text[0].Type != UIMessageText {
		t.Fatalf("text messages = %#v", text)
	}
	if reasoning[0].ID >= text[0].ID {
		t.Fatalf("reasoning id %d must sort before text id %d", reasoning[0].ID, text[0].ID)
	}
}

func TestUIMessageStreamConverterUserInputRequest(t *testing.T) {
	converter := NewUIMessageStreamConverter()
	messages := converter.HandleEvent(UIMessageStreamEvent{
		Type:        "user_input_request",
		ToolName:    "ask_user",
		ToolCallID:  "call-ask",
		UserInputID: "input-1",
		ShortID:     3,
		Status:      "pending",
		Input:       map[string]any{"questions": []any{map[string]any{"text": "Which plan?", "kind": "multi_select"}}},
		Metadata: map[string]any{
			"user_input_id": "input-1",
			"ui_payload": map[string]any{
				"version": 2,
				"questions": []any{
					map[string]any{
						"id":   "q1",
						"text": "Which plan?",
						"kind": "multi_select",
						"options": []any{
							map[string]any{"id": "q1.o1", "label": "Plan A"},
							map[string]any{"id": "q1.o2", "label": "Plan B"},
						},
						"allow_custom": true,
					},
				},
			},
		},
	})

	if len(messages) != 1 {
		t.Fatalf("expected one message, got %d", len(messages))
	}
	msg := messages[0]
	if msg.UserInput == nil {
		t.Fatalf("missing user input: %#v", msg)
	}
	if msg.UserInput.UserInputID != "input-1" || len(msg.UserInput.Questions) != 1 {
		t.Fatalf("unexpected user input: %#v", msg.UserInput)
	}
	question := msg.UserInput.Questions[0]
	if question.Text != "Which plan?" || question.Kind != "multi_select" || !question.AllowCustom {
		t.Fatalf("unexpected question: %#v", question)
	}
	if len(question.Options) != 2 || question.Options[0].ID != "q1.o1" {
		t.Fatalf("unexpected options: %#v", question.Options)
	}
	if msg.Running == nil || *msg.Running {
		t.Fatalf("expected tool to stop running while waiting: %#v", msg)
	}
}

func TestConvertModelMessagesToUIAssistantMessagesIncludesUserInputMetadata(t *testing.T) {
	messages := ConvertModelMessagesToUIAssistantMessages([]turn.ModelMessage{{
		Role: "assistant",
		Content: mustUIRawJSON(t, []map[string]any{{
			"type":       "tool-call",
			"toolCallId": "call-ask",
			"toolName":   "ask_user",
			"input":      map[string]any{"question": "Which plan?"},
			"providerMetadata": map[string]any{
				"user_input": map[string]any{
					"user_input_id": "input-1",
					"short_id":      2,
					"status":        "pending",
					"ui_payload": map[string]any{
						"question":       "Which plan?",
						"selection_type": "multi_select",
						"options": []any{
							map[string]any{"id": "a", "label": "Plan A", "value": "A"},
							map[string]any{"id": "custom", "label": "Custom answer", "input_type": "text", "placeholder": "Type an answer"},
						},
					},
				},
			},
		}}),
	}})

	if len(messages) != 1 {
		t.Fatalf("expected one message, got %d", len(messages))
	}
	userInput := messages[0].UserInput
	if userInput == nil {
		t.Fatalf("missing user input: %#v", messages[0])
	}
	if userInput.UserInputID != "input-1" || userInput.ShortID != 2 || !userInput.CanRespond {
		t.Fatalf("unexpected user input: %#v", userInput)
	}
	// Stored legacy (v1) payloads upgrade on read: selection_type becomes
	// multi_select and the v1 custom-text option becomes allow_custom.
	if len(userInput.Questions) != 1 {
		t.Fatalf("expected one upgraded question: %#v", userInput.Questions)
	}
	question := userInput.Questions[0]
	if question.Kind != "multi_select" || !question.AllowCustom || question.Placeholder != "Type an answer" {
		t.Fatalf("unexpected legacy upgrade: %#v", question)
	}
	if len(question.Options) != 1 || question.Options[0].ID != "a" || question.Options[0].Label != "Plan A" {
		t.Fatalf("unexpected options: %#v", question.Options)
	}
}

func TestConvertModelMessagesToUIAssistantMessagesIncludesExecutionLocation(t *testing.T) {
	t.Parallel()

	messages := ConvertModelMessagesToUIAssistantMessages([]turn.ModelMessage{{
		Role: "assistant",
		Content: mustUIRawJSON(t, []map[string]any{{
			"type":       "tool-call",
			"toolCallId": "call-1",
			"toolName":   "exec",
			"input":      map[string]any{"command": "pwd"},
			"providerMetadata": map[string]any{
				"execution_location": map[string]any{
					"kind": "remote",
					"name": "Office Mac",
				},
			},
		}}),
	}})

	if len(messages) != 1 || messages[0].ExecutionLocation == nil {
		t.Fatalf("messages = %#v, want execution location", messages)
	}
	if got := *messages[0].ExecutionLocation; got.Kind != "remote" || got.Name != "Office Mac" {
		t.Fatalf("execution location = %#v", got)
	}
}

func TestConvertMessagesToUITurnsIncludesReplyAndForwardMetadata(t *testing.T) {
	now := time.Now().UTC()
	turns := convertTestMessagesToUITurns([]messagepkg.Message{{
		ID:                     "user-1",
		BotID:                  "bot-1",
		SessionID:              "session-1",
		Role:                   "user",
		ExternalMessageID:      "external-user-1",
		SourceReplyToMessageID: "reply-1",
		Content: mustUIMessageJSON(t, turn.ModelMessage{
			Role:    "user",
			Content: mustUIRawJSON(t, "hello"),
		}),
		Metadata: map[string]any{
			"reply": map[string]any{
				"message_id": "reply-1",
				"sender":     "Original Sender",
				"preview":    "quoted text",
				"attachments": []map[string]any{{
					"type":         "image",
					"content_hash": "image-hash",
					"mime":         "image/png",
					"name":         "quoted.png",
					"metadata":     map[string]any{"bot_id": "bot-1", "storage_key": "im/image-hash.png"},
				}},
			},
			"forward": map[string]any{
				"message_id":           "forward-1",
				"from_conversation_id": "source-conversation",
				"sender":               "Source Channel",
				"date":                 float64(1710000000),
			},
		},
		CreatedAt: now,
	}})

	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if turns[0].ExternalMessageID != "external-user-1" {
		t.Fatalf("unexpected external message id: %q", turns[0].ExternalMessageID)
	}
	if turns[0].Reply == nil || turns[0].Reply.MessageID != "reply-1" || turns[0].Reply.Preview != "quoted text" {
		t.Fatalf("unexpected reply metadata: %#v", turns[0].Reply)
	}
	if len(turns[0].Reply.Attachments) != 1 || turns[0].Reply.Attachments[0].ContentHash != "image-hash" {
		t.Fatalf("unexpected reply attachments: %#v", turns[0].Reply.Attachments)
	}
	if turns[0].Forward == nil || turns[0].Forward.MessageID != "forward-1" || turns[0].Forward.Sender != "Source Channel" {
		t.Fatalf("unexpected forward metadata: %#v", turns[0].Forward)
	}
}

func TestConvertMessagesToUITurnsParsesRawReplyMetadata(t *testing.T) {
	now := time.Now().UTC()
	turns := convertTestMessagesToUITurns([]messagepkg.Message{{
		ID:        "user-raw-reply",
		BotID:     "bot-1",
		SessionID: "session-1",
		Role:      "user",
		Content: mustUIMessageJSON(t, turn.ModelMessage{
			Role:    "user",
			Content: mustUIRawJSON(t, "hello"),
		}),
		RawMetadata: json.RawMessage(`{"reply":{"message_id":"reply-raw","sender":"Original Sender","preview":"quoted text"}}`),
		CreatedAt:   now,
	}})

	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if turns[0].Reply == nil || turns[0].Reply.MessageID != "reply-raw" || turns[0].Reply.Preview != "quoted text" {
		t.Fatalf("unexpected reply metadata: %#v", turns[0].Reply)
	}
}

func TestConvertMessagesToUITurnsTruncatesReplyPreview(t *testing.T) {
	now := time.Now().UTC()
	longPreview := strings.Repeat("预览", 80)
	turns := convertTestMessagesToUITurns([]messagepkg.Message{{
		ID:        "user-1",
		BotID:     "bot-1",
		SessionID: "session-1",
		Role:      "user",
		Content: mustUIMessageJSON(t, turn.ModelMessage{
			Role:    "user",
			Content: mustUIRawJSON(t, "hello"),
		}),
		Metadata: map[string]any{
			"reply": map[string]any{
				"message_id": "reply-1",
				"preview":    longPreview,
			},
		},
		CreatedAt: now,
	}})
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if turns[0].Reply == nil {
		t.Fatal("expected reply metadata")
	}
	if got := len([]rune(turns[0].Reply.Preview)); got > uiReplyPreviewMaxRunes {
		t.Fatalf("reply preview too long: %d", got)
	}
	if !strings.HasSuffix(turns[0].Reply.Preview, "...") {
		t.Fatalf("expected ellipsis suffix, got %q", turns[0].Reply.Preview)
	}
}

func TestConvertMessagesToUITurnsKeepsForwardOnlyUserMessage(t *testing.T) {
	now := time.Now().UTC()
	turns := convertTestMessagesToUITurns([]messagepkg.Message{{
		ID:        "user-1",
		BotID:     "bot-1",
		Role:      "user",
		Content:   json.RawMessage(`{"role":"user","content":[{"type":"text","text":""}]}`),
		Metadata:  map[string]any{"forward": map[string]any{"message_id": "forward-1", "sender": "Source"}},
		CreatedAt: now,
	}})
	if len(turns) != 1 {
		t.Fatalf("expected one turn, got %d", len(turns))
	}
	if turns[0].Forward == nil || turns[0].Forward.MessageID != "forward-1" {
		t.Fatalf("unexpected forward metadata: %#v", turns[0].Forward)
	}
}

func TestConvertMessagesToUITurnsKeepsSkillActivationWithoutPrompt(t *testing.T) {
	now := time.Now().UTC()
	turns := convertTestMessagesToUITurns([]messagepkg.Message{{
		ID:             "user-activation",
		BotID:          "bot-1",
		Role:           "user",
		Content:        json.RawMessage(`{"role":"user","content":[{"type":"text","text":""}]}`),
		DisplayContent: "",
		Metadata: map[string]any{
			"user_message_kind": turn.UserMessageKindSkillActivation,
			"skill_activation": map[string]any{
				"skills": []any{map[string]any{
					"name":         "alpha",
					"display_name": "Alpha",
					"description":  "safe summary",
					"source_kind":  "managed",
					"state":        "effective",
				}},
			},
		},
		CreatedAt: now,
	}})
	if len(turns) != 1 {
		t.Fatalf("expected one turn, got %d", len(turns))
	}
	if turns[0].UserMessageKind != turn.UserMessageKindSkillActivation {
		t.Fatalf("kind = %q, want skill_activation", turns[0].UserMessageKind)
	}
	if turns[0].Text != "" {
		t.Fatalf("text = %q, want empty display text", turns[0].Text)
	}
	if turns[0].SkillActivation == nil || len(turns[0].SkillActivation.Skills) != 1 {
		t.Fatalf("skill activation = %#v, want one skill", turns[0].SkillActivation)
	}
	if turns[0].SkillActivation.Skills[0].Name != "alpha" || turns[0].SkillActivation.Skills[0].DisplayName != "Alpha" {
		t.Fatalf("unexpected skill activation payload: %#v", turns[0].SkillActivation.Skills[0])
	}
}

func TestConvertMessagesToUITurnsInfersLegacyRequestedSkillActivation(t *testing.T) {
	now := time.Now().UTC()
	turns := convertTestMessagesToUITurns([]messagepkg.Message{{
		ID:             "user-activation",
		BotID:          "bot-1",
		Role:           "user",
		Content:        json.RawMessage(`{"role":"user","content":[{"type":"text","text":""}]}`),
		DisplayContent: "",
		Metadata: map[string]any{
			"model_requested_skills": []any{map[string]any{
				"name":        "legacy-alpha",
				"source_kind": "managed",
			}},
		},
		CreatedAt: now,
	}})
	if len(turns) != 1 {
		t.Fatalf("expected one turn, got %d", len(turns))
	}
	if turns[0].UserMessageKind != turn.UserMessageKindSkillActivation {
		t.Fatalf("kind = %q, want skill_activation", turns[0].UserMessageKind)
	}
	if turns[0].Text != "" {
		t.Fatalf("text = %q, want empty display text", turns[0].Text)
	}
	if turns[0].SkillActivation == nil || len(turns[0].SkillActivation.Skills) != 1 {
		t.Fatalf("skill activation = %#v, want one skill", turns[0].SkillActivation)
	}
	skill := turns[0].SkillActivation.Skills[0]
	if skill.Name != "legacy-alpha" || skill.SourceKind != "managed" {
		t.Fatalf("unexpected inferred skill activation payload: %#v", skill)
	}
}

func TestConvertMessagesToUITurnsStripsLegacyRawSlashActivationText(t *testing.T) {
	now := time.Now().UTC()
	turns := convertTestMessagesToUITurns([]messagepkg.Message{
		{
			ID:      "user-activation-empty",
			BotID:   "bot-1",
			Role:    "user",
			Content: json.RawMessage(`{"role":"user","content":[{"type":"text","text":"/legacy-alpha"}]}`),
			Metadata: map[string]any{
				"model_requested_skills": []any{map[string]any{"name": "legacy-alpha"}},
			},
			CreatedAt: now,
		},
		{
			ID:             "user-activation-prompt",
			BotID:          "bot-1",
			Role:           "user",
			Content:        json.RawMessage(`{"role":"user","content":[{"type":"text","text":""}]}`),
			DisplayContent: "/legacy-alpha please add widgets",
			Metadata: map[string]any{
				"model_requested_skills": []any{map[string]any{"name": "legacy-alpha"}},
			},
			CreatedAt: now.Add(time.Second),
		},
		{
			ID:      "user-activation-marker",
			BotID:   "bot-1",
			Role:    "user",
			Content: json.RawMessage(`{"role":"user","content":[{"type":"text","text":"The user activated the following skill for this turn without an additional prompt: legacy-alpha."}]}`),
			Metadata: map[string]any{
				"model_requested_skills": []any{map[string]any{"name": "legacy-alpha"}},
			},
			CreatedAt: now.Add(2 * time.Second),
		},
	})
	if len(turns) != 3 {
		t.Fatalf("expected three turns, got %d", len(turns))
	}
	if turns[0].Text != "" {
		t.Fatalf("first text = %q, want empty text without raw slash", turns[0].Text)
	}
	if turns[1].Text != "please add widgets" {
		t.Fatalf("second text = %q, want stripped prompt", turns[1].Text)
	}
	if turns[2].Text != "" {
		t.Fatalf("third text = %q, want empty text without model marker", turns[2].Text)
	}
	for _, uiTurn := range turns {
		if uiTurn.UserMessageKind != turn.UserMessageKindSkillActivation {
			t.Fatalf("kind = %q, want skill_activation", uiTurn.UserMessageKind)
		}
	}
}

func TestUIMessageStreamConverterAccumulatesToolProgress(t *testing.T) {
	converter := NewUIMessageStreamConverter()

	start := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_start",
		ToolName:   "exec",
		ToolCallID: "call-1",
		Input:      map[string]any{"command": "ls"},
	})
	if len(start) != 1 || start[0].Type != UIMessageTool || start[0].Name != "exec" {
		t.Fatalf("unexpected tool start event: %#v", start)
	}
	if start[0].Running == nil || !*start[0].Running {
		t.Fatalf("expected running tool start, got %#v", start[0])
	}

	progressOne := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_progress",
		ToolName:   "exec",
		ToolCallID: "call-1",
		Progress:   "line 1",
	})
	progressTwo := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_progress",
		ToolName:   "exec",
		ToolCallID: "call-1",
		Progress:   map[string]any{"line": 2},
	})
	if len(progressOne) != 1 || len(progressOne[0].Progress) != 1 {
		t.Fatalf("unexpected first progress snapshot: %#v", progressOne)
	}
	if len(progressTwo) != 1 || len(progressTwo[0].Progress) != 2 {
		t.Fatalf("unexpected second progress snapshot: %#v", progressTwo)
	}
	if progressTwo[0].ID != start[0].ID {
		t.Fatalf("expected progress snapshots to reuse tool message id")
	}

	end := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_end",
		ToolName:   "exec",
		ToolCallID: "call-1",
		Output:     map[string]any{"structuredContent": map[string]any{"stdout": "done"}},
	})
	if len(end) != 1 || end[0].Running == nil || *end[0].Running {
		t.Fatalf("expected completed tool snapshot, got %#v", end)
	}
	if end[0].ID != start[0].ID || len(end[0].Progress) != 2 {
		t.Fatalf("expected final snapshot to keep id and progress, got %#v", end[0])
	}
}

func TestUIMessageStreamConverterAddsExecutionLocationToExistingTool(t *testing.T) {
	t.Parallel()

	converter := NewUIMessageStreamConverter()
	start := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_start",
		ToolName:   "exec",
		ToolCallID: "call-1",
		Input:      map[string]any{"command": "pwd"},
	})
	update := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_metadata",
		ToolName:   "exec",
		ToolCallID: "call-1",
		Metadata: map[string]any{
			"execution_location": map[string]any{
				"kind": "remote",
				"name": "Office Mac",
			},
		},
	})

	if len(start) != 1 || len(update) != 1 || update[0].ID != start[0].ID {
		t.Fatalf("start/update = %#v/%#v, want one stable tool block", start, update)
	}
	if update[0].ExecutionLocation == nil || update[0].ExecutionLocation.Name != "Office Mac" {
		t.Fatalf("execution location = %#v", update[0].ExecutionLocation)
	}
}

func TestUIMessageStreamConverterUpdatesToolApprovalDecision(t *testing.T) {
	t.Parallel()

	converter := NewUIMessageStreamConverter()
	pending := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_approval_request",
		ToolName:   "exec",
		ToolCallID: "call-1",
		Input:      map[string]any{"command": "pwd"},
		ApprovalID: "approval-1",
		ShortID:    7,
		Status:     "pending",
	})
	if len(pending) != 1 || pending[0].Approval == nil || !pending[0].Approval.CanApprove {
		t.Fatalf("pending approval snapshot = %#v", pending)
	}

	approved := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_approval_request",
		ToolName:   "exec",
		ToolCallID: "call-1",
		Input:      map[string]any{"command": "pwd"},
		ApprovalID: "approval-1",
		ShortID:    7,
		Status:     "approved",
	})
	if len(approved) != 1 || approved[0].ID != pending[0].ID {
		t.Fatalf("approved approval snapshot = %#v, want same tool block", approved)
	}
	if approved[0].Approval == nil || approved[0].Approval.Status != "approved" || approved[0].Approval.CanApprove {
		t.Fatalf("approved approval state = %#v", approved[0].Approval)
	}
}

func TestUIMessageStreamConverterMergesRepeatedToolCallStart(t *testing.T) {
	t.Parallel()

	converter := NewUIMessageStreamConverter()

	start := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_start",
		ToolName:   "write",
		ToolCallID: "call-1",
	})
	if len(start) != 1 || start[0].Type != UIMessageTool {
		t.Fatalf("unexpected initial tool placeholder: %#v", start)
	}
	if start[0].Input != nil {
		t.Fatalf("expected initial tool placeholder to have nil input, got %#v", start[0].Input)
	}

	fullInput := map[string]any{"path": "/tmp/long.txt"}
	update := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_start",
		ToolName:   "write",
		ToolCallID: "call-1",
		Input:      fullInput,
	})
	if len(update) != 1 {
		t.Fatalf("expected one updated tool snapshot, got %#v", update)
	}
	if update[0].ID != start[0].ID {
		t.Fatalf("expected repeated tool start to reuse message id, got start=%d update=%d", start[0].ID, update[0].ID)
	}
	if !reflect.DeepEqual(update[0].Input, fullInput) {
		t.Fatalf("expected repeated tool start to backfill input, got %#v", update[0].Input)
	}
	if update[0].Running == nil || !*update[0].Running {
		t.Fatalf("expected merged tool message to stay running, got %#v", update[0])
	}

	end := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_end",
		ToolName:   "write",
		ToolCallID: "call-1",
		Output:     map[string]any{"ok": true},
	})
	if len(end) != 1 || end[0].ID != start[0].ID {
		t.Fatalf("expected tool end to reuse merged message id, got %#v", end)
	}
	if !reflect.DeepEqual(end[0].Input, fullInput) {
		t.Fatalf("expected tool end to preserve merged input, got %#v", end[0].Input)
	}
	if end[0].Running == nil || *end[0].Running {
		t.Fatalf("expected tool end to mark message complete, got %#v", end[0])
	}
}

func TestUIMessageStreamConverterToolCallInputStartThenStartBackfillsInput(t *testing.T) {
	t.Parallel()

	converter := NewUIMessageStreamConverter()

	inputStart := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_input_start",
		ToolName:   "write",
		ToolCallID: "call-1",
	})
	if len(inputStart) != 1 || inputStart[0].Type != UIMessageTool {
		t.Fatalf("unexpected initial tool placeholder: %#v", inputStart)
	}
	if inputStart[0].Input != nil {
		t.Fatalf("expected input-start placeholder to have nil input, got %#v", inputStart[0].Input)
	}
	if inputStart[0].Running == nil || !*inputStart[0].Running {
		t.Fatalf("expected input-start placeholder to be running, got %#v", inputStart[0])
	}

	fullInput := map[string]any{"path": "/tmp/long.txt"}
	start := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_start",
		ToolName:   "write",
		ToolCallID: "call-1",
		Input:      fullInput,
	})
	if len(start) != 1 {
		t.Fatalf("expected one updated tool snapshot, got %#v", start)
	}
	if start[0].ID != inputStart[0].ID {
		t.Fatalf("expected tool start to reuse message id, got input-start=%d start=%d", inputStart[0].ID, start[0].ID)
	}
	if !reflect.DeepEqual(start[0].Input, fullInput) {
		t.Fatalf("expected tool start to backfill input, got %#v", start[0].Input)
	}
	if start[0].Running == nil || !*start[0].Running {
		t.Fatalf("expected merged tool message to stay running, got %#v", start[0])
	}

	end := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_end",
		ToolName:   "write",
		ToolCallID: "call-1",
		Output:     map[string]any{"ok": true},
	})
	if len(end) != 1 || end[0].ID != inputStart[0].ID {
		t.Fatalf("expected tool end to reuse merged message id, got %#v", end)
	}
	if !reflect.DeepEqual(end[0].Input, fullInput) {
		t.Fatalf("expected tool end to preserve merged input, got %#v", end[0].Input)
	}
	if end[0].Running == nil || *end[0].Running {
		t.Fatalf("expected tool end to mark message complete, got %#v", end[0])
	}
}

func TestUIMessageStreamConverterKeepsParallelSameNameToolCallsSeparate(t *testing.T) {
	t.Parallel()

	converter := NewUIMessageStreamConverter()

	startA := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_start",
		ToolName:   "search",
		ToolCallID: "call-A",
		Input:      map[string]any{"query": "A"},
	})
	startB := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_start",
		ToolName:   "search",
		ToolCallID: "call-B",
		Input:      map[string]any{"query": "B"},
	})
	startC := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_start",
		ToolName:   "search",
		ToolCallID: "call-C",
		Input:      map[string]any{"query": "C"},
	})

	if len(startA) != 1 || len(startB) != 1 || len(startC) != 1 {
		t.Fatalf("expected one snapshot per start, got A=%#v B=%#v C=%#v", startA, startB, startC)
	}
	if startA[0].ID == startB[0].ID || startB[0].ID == startC[0].ID || startA[0].ID == startC[0].ID {
		t.Fatalf("expected each parallel tool call to receive a distinct id, got A=%d B=%d C=%d",
			startA[0].ID, startB[0].ID, startC[0].ID)
	}
	if startA[0].ToolCallID != "call-A" || startB[0].ToolCallID != "call-B" || startC[0].ToolCallID != "call-C" {
		t.Fatalf("expected each snapshot to keep its own tool_call_id, got A=%q B=%q C=%q",
			startA[0].ToolCallID, startB[0].ToolCallID, startC[0].ToolCallID)
	}

	endA := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_end",
		ToolName:   "search",
		ToolCallID: "call-A",
		Output:     map[string]any{"hits": "A"},
	})
	endB := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_end",
		ToolName:   "search",
		ToolCallID: "call-B",
		Output:     map[string]any{"hits": "B"},
	})
	endC := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_end",
		ToolName:   "search",
		ToolCallID: "call-C",
		Output:     map[string]any{"hits": "C"},
	})

	if endA[0].ID != startA[0].ID || endB[0].ID != startB[0].ID || endC[0].ID != startC[0].ID {
		t.Fatalf("expected tool_call_end to reuse the matching start id, got endA=%d endB=%d endC=%d (startA=%d startB=%d startC=%d)",
			endA[0].ID, endB[0].ID, endC[0].ID, startA[0].ID, startB[0].ID, startC[0].ID)
	}
	if !reflect.DeepEqual(endA[0].Input, map[string]any{"query": "A"}) ||
		!reflect.DeepEqual(endB[0].Input, map[string]any{"query": "B"}) ||
		!reflect.DeepEqual(endC[0].Input, map[string]any{"query": "C"}) {
		t.Fatalf("expected each tool_call_end to preserve its own input, got A=%#v B=%#v C=%#v",
			endA[0].Input, endB[0].Input, endC[0].Input)
	}
}

func TestUIMessageStreamConverterStartsNewTextBlockAfterTool(t *testing.T) {
	converter := NewUIMessageStreamConverter()

	first := converter.HandleEvent(UIMessageStreamEvent{Type: "text_delta", Delta: "hello"})
	converter.HandleEvent(UIMessageStreamEvent{Type: "tool_call_start", ToolName: "read", ToolCallID: "call-1"})
	converter.HandleEvent(UIMessageStreamEvent{Type: "tool_call_end", ToolName: "read", ToolCallID: "call-1"})
	second := converter.HandleEvent(UIMessageStreamEvent{Type: "text_delta", Delta: "world"})

	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected text snapshots, got first=%#v second=%#v", first, second)
	}
	if first[0].ID == second[0].ID {
		t.Fatalf("expected new text block after tool call, got same id %d", first[0].ID)
	}
}

func TestConvertRawModelMessagesToUIAssistantMessagesBuildsTerminalSnapshots(t *testing.T) {
	raw := mustUIRawJSON(t, []turn.ModelMessage{
		{
			Role: "assistant",
			Content: mustUIRawJSON(t, []map[string]any{
				{"type": "reasoning", "text": "thinking"},
				{"type": "tool-call", "toolCallId": "call-1", "toolName": "read", "input": map[string]any{"path": "/tmp/a.txt"}},
			}),
		},
		{
			Role: "tool",
			Content: mustUIRawJSON(t, []map[string]any{
				{"type": "tool-result", "toolCallId": "call-1", "toolName": "read", "result": map[string]any{"structuredContent": map[string]any{"stdout": "ok"}}},
			}),
		},
		{
			Role: "assistant",
			Content: mustUIRawJSON(t, []map[string]any{
				{"type": "text", "text": "final answer"},
			}),
		},
	})

	messages := ConvertRawModelMessagesToUIAssistantMessages(raw)
	if len(messages) != 3 {
		t.Fatalf("expected 3 ui messages, got %d", len(messages))
	}
	if messages[0].ID != 0 || messages[0].Type != UIMessageReasoning {
		t.Fatalf("unexpected first ui message: %#v", messages[0])
	}
	if messages[1].ID != 1 || messages[1].Type != UIMessageTool {
		t.Fatalf("unexpected second ui message: %#v", messages[1])
	}
	if messages[1].Running == nil || *messages[1].Running {
		t.Fatalf("expected terminal tool message to be completed: %#v", messages[1])
	}
	if messages[2].ID != 2 || messages[2].Type != UIMessageText || messages[2].Content != "final answer" {
		t.Fatalf("unexpected final ui message: %#v", messages[2])
	}
}

func TestConvertRawModelMessagesToUIAssistantMessagesKeepsBackgroundExecRunning(t *testing.T) {
	raw := mustUIRawJSON(t, []turn.ModelMessage{
		{
			Role: "assistant",
			Content: mustUIRawJSON(t, []map[string]any{
				{"type": "tool-call", "toolCallId": "call-1", "toolName": "exec", "input": map[string]any{"command": "npm test"}},
			}),
		},
		{
			Role: "tool",
			Content: mustUIRawJSON(t, []map[string]any{
				{"type": "tool-result", "toolCallId": "call-1", "toolName": "exec", "result": map[string]any{"structuredContent": map[string]any{"status": "background_started", "task_id": "bg_1", "output_file": "/tmp/memoh-bg/bg_1.log"}}},
			}),
		},
	})

	messages := ConvertRawModelMessagesToUIAssistantMessages(raw)
	if len(messages) != 1 {
		t.Fatalf("expected one exec tool message, got %d", len(messages))
	}
	if messages[0].Running == nil || !*messages[0].Running {
		t.Fatalf("expected background exec to remain running: %#v", messages[0])
	}
	if messages[0].Background == nil || messages[0].Background.TaskID != "bg_1" {
		t.Fatalf("expected background metadata on exec block: %#v", messages[0])
	}
}

func TestApplyBackgroundTaskSnapshotsClosesPersistedStartedExec(t *testing.T) {
	baseTime := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	messages := []messagepkg.Message{
		{
			ID:        "assistant-1",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "assistant",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role: "assistant",
				Content: mustUIRawJSON(t, []map[string]any{
					{"type": "tool-call", "toolCallId": "call-1", "toolName": "exec", "input": map[string]any{"command": "npm test"}},
				}),
			}),
			CreatedAt: baseTime,
		},
		{
			ID:        "tool-1",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "tool",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role: "tool",
				Content: mustUIRawJSON(t, []map[string]any{
					{"type": "tool-result", "toolCallId": "call-1", "toolName": "exec", "result": map[string]any{"structuredContent": map[string]any{"status": "background_started", "task_id": "bg_1", "output_file": "/tmp/memoh-bg/bg_1.log"}}},
				}),
			}),
			CreatedAt: baseTime.Add(time.Second),
		},
	}

	turns := convertTestMessagesToUITurns(messages)
	if len(turns) != 1 || len(turns[0].Messages) != 1 {
		t.Fatalf("unexpected initial turns: %#v", turns)
	}
	tool := turns[0].Messages[0]
	if tool.Running == nil || !*tool.Running {
		t.Fatalf("expected persisted background_started tool to be running: %#v", tool)
	}

	ApplyBackgroundTaskSnapshots(turns, []UIBackgroundTask{{
		TaskID:     "bg_1",
		Status:     "completed",
		Command:    "npm test",
		OutputFile: "/tmp/memoh-bg/bg_1.log",
		Duration:   "2s",
		OutputTail: "ok\n",
	}})

	tool = turns[0].Messages[0]
	if tool.Running == nil || *tool.Running {
		t.Fatalf("expected snapshot to close exec tool: %#v", tool)
	}
	if tool.Background == nil || tool.Background.Status != "completed" || tool.Background.OutputTail != "ok\n" {
		t.Fatalf("unexpected snapshot merge: %#v", tool.Background)
	}
}

func TestApplyBackgroundTaskSnapshotsClosesMissingPersistedStartedExec(t *testing.T) {
	baseTime := time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC)
	messages := []messagepkg.Message{
		{
			ID:        "assistant-1",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "assistant",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role: "assistant",
				Content: mustUIRawJSON(t, []map[string]any{
					{"type": "tool-call", "toolCallId": "call-1", "toolName": "exec", "input": map[string]any{"command": "npm test"}},
				}),
			}),
			CreatedAt: baseTime,
		},
		{
			ID:        "tool-1",
			BotID:     "bot-1",
			SessionID: "session-1",
			Role:      "tool",
			Content: mustUIMessageJSON(t, turn.ModelMessage{
				Role: "tool",
				Content: mustUIRawJSON(t, []map[string]any{
					{"type": "tool-result", "toolCallId": "call-1", "toolName": "exec", "result": map[string]any{"structuredContent": map[string]any{"status": "background_started", "task_id": "bg_1", "output_file": "/tmp/memoh-bg/bg_1.log"}}},
				}),
			}),
			CreatedAt: baseTime.Add(time.Second),
		},
	}

	turns := convertTestMessagesToUITurns(messages)
	if len(turns) != 1 || len(turns[0].Messages) != 1 {
		t.Fatalf("unexpected initial turns: %#v", turns)
	}
	tool := turns[0].Messages[0]
	if tool.Running == nil || !*tool.Running {
		t.Fatalf("expected persisted background_started tool to be running: %#v", tool)
	}

	ApplyBackgroundTaskSnapshots(turns, nil)

	tool = turns[0].Messages[0]
	if tool.Running == nil || *tool.Running {
		t.Fatalf("expected missing snapshot to close exec tool: %#v", tool)
	}
	if tool.Background == nil || tool.Background.Status != "unknown" {
		t.Fatalf("expected missing snapshot to mark background task unknown: %#v", tool.Background)
	}
}

func TestApplyBackgroundTaskSnapshotsKeepsLiveTaskAndClosesMissingTask(t *testing.T) {
	turns := []UITurn{{
		Role: "assistant",
		Messages: []UIMessage{
			{
				ID:         1,
				Type:       UIMessageTool,
				Name:       "exec",
				ToolCallID: "call-live",
				Running:    uiBoolPtr(true),
				Background: &UIBackgroundTask{
					TaskID: "bg-live",
					Status: "running",
				},
			},
			{
				ID:         2,
				Type:       UIMessageTool,
				Name:       "exec",
				ToolCallID: "call-missing",
				Running:    uiBoolPtr(true),
				Background: &UIBackgroundTask{
					TaskID: "bg-missing",
					Status: "running",
				},
			},
		},
	}}

	ApplyBackgroundTaskSnapshots(turns, []UIBackgroundTask{{
		TaskID: "bg-live",
		Status: "running",
	}})

	live := turns[0].Messages[0]
	if live.Running == nil || !*live.Running || live.Background == nil || live.Background.Status != "running" {
		t.Fatalf("expected live snapshot to stay running: %#v", live)
	}
	missing := turns[0].Messages[1]
	if missing.Running == nil || *missing.Running || missing.Background == nil || missing.Background.Status != "unknown" {
		t.Fatalf("expected missing snapshot to close as unknown: %#v", missing)
	}
}

func TestApplyBackgroundTaskSnapshotsDoesNotRewriteMissingTerminalTask(t *testing.T) {
	turns := []UITurn{{
		Role: "assistant",
		Messages: []UIMessage{{
			ID:         1,
			Type:       UIMessageTool,
			Name:       "exec",
			ToolCallID: "call-completed",
			Running:    uiBoolPtr(false),
			Background: &UIBackgroundTask{
				TaskID:     "bg-completed",
				Status:     "completed",
				OutputTail: "ok\n",
			},
		}},
	}}

	ApplyBackgroundTaskSnapshots(turns, nil)

	tool := turns[0].Messages[0]
	if tool.Running == nil || *tool.Running {
		t.Fatalf("expected terminal tool to stay closed: %#v", tool)
	}
	if tool.Background == nil || tool.Background.Status != "completed" || tool.Background.OutputTail != "ok\n" {
		t.Fatalf("expected missing terminal task to remain unchanged: %#v", tool.Background)
	}
}

func mustUIRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal raw json: %v", err)
	}
	return data
}

func mustUIMessageJSON(t *testing.T, message turn.ModelMessage) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	return data
}

func TestConvertTerminalMessagesReusesLiveBlockIDsAroundAttachments(t *testing.T) {
	converter := NewUIMessageStreamConverter()

	// Live stream: tool call starts, emits an attachment mid-execution, then
	// the closing text streams in. IDs: tool=0, attachments=1, text=2.
	converter.HandleEvent(UIMessageStreamEvent{Type: "tool_call_start", ToolCallID: "call-1", ToolName: "generate_image"})
	attachmentBlocks := converter.HandleEvent(UIMessageStreamEvent{Type: "attachment_delta", Attachments: []UIAttachment{{Type: "image", ContentHash: "hash-1"}}})
	if len(attachmentBlocks) != 1 || attachmentBlocks[0].ID != 1 {
		t.Fatalf("attachment block = %#v, want id 1", attachmentBlocks)
	}
	converter.HandleEvent(UIMessageStreamEvent{Type: "tool_call_end", ToolCallID: "call-1", ToolName: "generate_image"})
	textBlocks := converter.HandleEvent(UIMessageStreamEvent{Type: "text_delta", Delta: "done"})
	if len(textBlocks) != 1 || textBlocks[0].ID != 2 {
		t.Fatalf("text block = %#v, want id 2", textBlocks)
	}

	// Terminal snapshot regenerates only tool + text; without ID alignment the
	// text would land on id 1 and overwrite the attachment block client-side.
	raw := mustUIRawJSON(t, []turn.ModelMessage{
		{
			Role: "assistant",
			Content: mustUIRawJSON(t, []map[string]any{
				{"type": "tool-call", "toolCallId": "call-1", "toolName": "generate_image", "input": map[string]any{"prompt": "a cat"}},
			}),
		},
		{
			Role: "tool",
			Content: mustUIRawJSON(t, []map[string]any{
				{"type": "tool-result", "toolCallId": "call-1", "toolName": "generate_image", "result": map[string]any{"path": "/data/generated-images/1.png"}},
			}),
		},
		{
			Role: "assistant",
			Content: mustUIRawJSON(t, []map[string]any{
				{"type": "text", "text": "done"},
			}),
		},
	})

	blocks := converter.ConvertTerminalMessages(raw)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 terminal blocks, got %d: %#v", len(blocks), blocks)
	}
	if blocks[0].Type != UIMessageTool || blocks[0].ID != 0 {
		t.Fatalf("tool block = %#v, want live id 0", blocks[0])
	}
	if blocks[1].Type != UIMessageText || blocks[1].ID != 2 {
		t.Fatalf("text block = %#v, want live id 2 (must not collide with attachment id 1)", blocks[1])
	}
}

func TestConvertTerminalMessagesAllocatesFreshIDsForUnseenBlocks(t *testing.T) {
	converter := NewUIMessageStreamConverter()

	// No live blocks at all (e.g. events dropped): snapshot still renders with
	// sequential fresh IDs.
	raw := mustUIRawJSON(t, []turn.ModelMessage{
		{
			Role: "assistant",
			Content: mustUIRawJSON(t, []map[string]any{
				{"type": "text", "text": "hello"},
			}),
		},
	})
	blocks := converter.ConvertTerminalMessages(raw)
	if len(blocks) != 1 || blocks[0].ID != 0 || blocks[0].Type != UIMessageText {
		t.Fatalf("blocks = %#v, want one fresh text block with id 0", blocks)
	}
}

func TestConvertTerminalMessagesAfterRetryIgnoresDiscardedAttemptBlocks(t *testing.T) {
	converter := NewUIMessageStreamConverter()

	// Attempt 1 streams a text block (ID 0), then the attempt is retried.
	converter.HandleEvent(UIMessageStreamEvent{Type: "text_delta", Delta: "partial attempt one"})
	converter.HandleEvent(UIMessageStreamEvent{Type: "retry"})

	// Attempt 2 streams tool (ID 1), an attachment (ID 2), and the final text (ID 3).
	converter.HandleEvent(UIMessageStreamEvent{Type: "tool_call_start", ToolCallID: "call-1", ToolName: "generate_image"})
	converter.HandleEvent(UIMessageStreamEvent{Type: "attachment_delta", Attachments: []UIAttachment{{Type: "image", ContentHash: "hash-1"}}})
	converter.HandleEvent(UIMessageStreamEvent{Type: "tool_call_end", ToolCallID: "call-1", ToolName: "generate_image"})
	textBlocks := converter.HandleEvent(UIMessageStreamEvent{Type: "text_delta", Delta: "final answer"})
	if len(textBlocks) != 1 {
		t.Fatalf("expected 1 text block, got %d", len(textBlocks))
	}
	finalTextID := textBlocks[0].ID

	// finalMessages hold only attempt 2 (tool-call + final text).
	raw := mustUIRawJSON(t, []turn.ModelMessage{
		{
			Role: "assistant",
			Content: mustUIRawJSON(t, []map[string]any{
				{"type": "tool-call", "toolCallId": "call-1", "toolName": "generate_image", "input": map[string]any{"prompt": "a cat"}},
			}),
		},
		{
			Role: "tool",
			Content: mustUIRawJSON(t, []map[string]any{
				{"type": "tool-result", "toolCallId": "call-1", "toolName": "generate_image", "result": map[string]any{"path": "/data/generated-images/1.png"}},
			}),
		},
		{
			Role: "assistant",
			Content: mustUIRawJSON(t, []map[string]any{
				{"type": "text", "text": "final answer"},
			}),
		},
	})

	blocks := converter.ConvertTerminalMessages(raw)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 terminal blocks, got %d: %#v", len(blocks), blocks)
	}
	// The final text must reuse the live final text block ID (attempt 2), not
	// the discarded attempt-1 text block (ID 0), so it does not overwrite it.
	var terminalText *UIMessage
	for i := range blocks {
		if blocks[i].Type == UIMessageText {
			terminalText = &blocks[i]
		}
	}
	if terminalText == nil {
		t.Fatal("expected a terminal text block")
	}
	if terminalText.ID != finalTextID {
		t.Fatalf("terminal text ID = %d, want live final text ID %d (retry must clear the discarded attempt's block)", terminalText.ID, finalTextID)
	}
	if terminalText.ID == 0 {
		t.Fatal("terminal text must not reuse the discarded attempt-1 block ID 0")
	}
}

func TestConvertTerminalMessagesSkipsTagOnlyLiveTextBlocks(t *testing.T) {
	converter := NewUIMessageStreamConverter()

	// Live: a standalone text segment that is entirely inline agent tags
	// (ID 0), then a tool call (ID 1), then the real reply (ID 2). The terminal
	// snapshot strips the tags and drops the empty text block, so it contains
	// only the tool call and the real reply.
	converter.HandleEvent(UIMessageStreamEvent{Type: "text_delta", Delta: `<attachments>{"path":"/tmp/a.png"}</attachments>`})
	converter.HandleEvent(UIMessageStreamEvent{Type: "text_end"})
	converter.HandleEvent(UIMessageStreamEvent{Type: "tool_call_start", ToolCallID: "call-1", ToolName: "generate_image"})
	converter.HandleEvent(UIMessageStreamEvent{Type: "tool_call_end", ToolCallID: "call-1", ToolName: "generate_image"})
	textBlocks := converter.HandleEvent(UIMessageStreamEvent{Type: "text_delta", Delta: "here you go"})
	if len(textBlocks) != 1 {
		t.Fatalf("expected 1 live text block, got %d", len(textBlocks))
	}
	realTextID := textBlocks[0].ID

	raw := mustUIRawJSON(t, []turn.ModelMessage{
		{
			Role: "assistant",
			Content: mustUIRawJSON(t, []map[string]any{
				{"type": "tool-call", "toolCallId": "call-1", "toolName": "generate_image", "input": map[string]any{"prompt": "a cat"}},
			}),
		},
		{
			Role: "tool",
			Content: mustUIRawJSON(t, []map[string]any{
				{"type": "tool-result", "toolCallId": "call-1", "toolName": "generate_image", "result": map[string]any{"path": "/tmp/a.png"}},
			}),
		},
		{
			Role: "assistant",
			Content: mustUIRawJSON(t, []map[string]any{
				{"type": "text", "text": "here you go"},
			}),
		},
	})

	blocks := converter.ConvertTerminalMessages(raw)
	var terminalText *UIMessage
	for i := range blocks {
		if blocks[i].Type == UIMessageText {
			terminalText = &blocks[i]
		}
	}
	if terminalText == nil {
		t.Fatal("expected a terminal text block")
	}
	if terminalText.ID != realTextID {
		t.Fatalf("terminal text ID = %d, want %d (must skip the tag-only live block, not overwrite it)", terminalText.ID, realTextID)
	}
}
