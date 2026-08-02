package application

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"
)

func TestProjectModelMessageHeadersProjectsUserTextWithoutMutatingHistory(t *testing.T) {
	t.Parallel()

	full := `<message id="42" sender="Alice" t="now" channel="telegram" type="private">hello</message>`
	messages := []ModelMessage{
		{Role: "user", Content: newTextContent(full)},
		{Role: "assistant", Content: newTextContent("reply")},
	}
	projected := projectModelMessageHeaders(messages, "compact")

	if got := projected[0].TextContent(); got != `<message id="42">hello</message>` {
		t.Fatalf("projected user message = %q", got)
	}
	if got := messages[0].TextContent(); got != full {
		t.Fatalf("canonical history was mutated: %q", got)
	}
	if got := projected[1].TextContent(); got != "reply" {
		t.Fatalf("assistant message changed: %q", got)
	}
}

func TestProjectSDKMessageHeadersProjectsDiscussMessagesWithoutMutation(t *testing.T) {
	t.Parallel()

	full := `<message id="44" sender="Alice" t="now" channel="telegram" type="group">hello</message>`
	messages := []sdk.Message{sdk.UserMessage(full, sdk.ImagePart{Image: "data:image/png;base64,abc"})}
	projected := projectSDKMessageHeaders(messages, "compact")

	textPart, ok := projected[0].Content[0].(sdk.TextPart)
	if !ok || textPart.Text != `<message id="44" sender="Alice">hello</message>` {
		t.Fatalf("projected SDK text = %#v", projected[0].Content[0])
	}
	originalText, ok := messages[0].Content[0].(sdk.TextPart)
	if !ok || originalText.Text != full {
		t.Fatalf("canonical SDK message was mutated: %#v", messages[0].Content[0])
	}
	if _, ok := projected[0].Content[1].(sdk.ImagePart); !ok {
		t.Fatalf("image part changed: %#v", projected[0].Content[1])
	}
}

func TestProjectModelMessageHeadersProjectsMultipartTextOnly(t *testing.T) {
	t.Parallel()

	parts, err := json.Marshal([]ContentPart{
		{Type: "text", Text: `<message id="43" sender="Alice" t="now" channel="telegram" type="group">hello</message>`},
		{Type: "image", URL: "https://example.test/image.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	projected := projectModelMessageHeaders([]ModelMessage{{Role: "user", Content: parts}}, "compact")
	got := projected[0].ContentParts()
	if len(got) != 2 {
		t.Fatalf("parts = %#v", got)
	}
	if got[0].Text != `<message id="43" sender="Alice">hello</message>` {
		t.Fatalf("projected text = %q", got[0].Text)
	}
	if !strings.Contains(got[1].URL, "image.png") {
		t.Fatalf("image part changed: %#v", got[1])
	}
}

func TestProjectDiscussToolHistoryCollapsesSuccessfulSendAndPrivateText(t *testing.T) {
	t.Parallel()

	messages := []sdk.Message{
		{
			Role: sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{
				sdk.TextPart{Text: "private deliberation that was never shown"},
				sdk.ToolCallPart{
					ToolCallID: "send-1",
					ToolName:   "send",
					Input:      map[string]any{"target": "chat-1", "text": "visible reply"},
				},
			},
		},
		sdk.ToolMessage(sdk.ToolResultPart{
			ToolCallID: "send-1",
			ToolName:   "send",
			Result:     map[string]any{"ok": true},
		}),
		sdk.AssistantMessage("private post-send note"),
		sdk.UserMessage("next message"),
	}

	projected := projectDiscussToolHistory(messages)
	if len(projected) != 2 {
		t.Fatalf("projected messages = %#v", projected)
	}
	visible, ok := projected[0].Content[0].(sdk.TextPart)
	if projected[0].Role != sdk.MessageRoleAssistant || !ok || visible.Text != "visible reply" {
		t.Fatalf("visible projection = %#v", projected[0])
	}
	if projected[1].Role != sdk.MessageRoleUser {
		t.Fatalf("last role = %q", projected[1].Role)
	}
	if got := messages[0].Content[0].(sdk.TextPart).Text; got != "private deliberation that was never shown" {
		t.Fatalf("canonical input mutated: %q", got)
	}
}

func TestProjectDiscussToolHistoryCollapsesStickerSearchAndCombinedSend(t *testing.T) {
	t.Parallel()

	messages := []sdk.Message{
		{
			Role: sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{sdk.ToolCallPart{
				ToolCallID: "search-1",
				ToolName:   "sticker_search_telegram_stickers",
				Input:      map[string]any{"query": "开心挥手"},
			}},
		},
		sdk.ToolMessage(sdk.ToolResultPart{
			ToolCallID: "search-1",
			ToolName:   "sticker_search_telegram_stickers",
			Result:     map[string]any{"structuredContent": map[string]any{"stickers": []any{}}},
		}),
		{
			Role: sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{sdk.ToolCallPart{
				ToolCallID: "sticker-1",
				ToolName:   "sticker_send_telegram_sticker",
				Input: map[string]any{
					"chat_id":    "chat-1",
					"text":       "晚安",
					"sticker_id": "S007",
				},
			}},
		},
		sdk.ToolMessage(sdk.ToolResultPart{
			ToolCallID: "sticker-1",
			ToolName:   "sticker_send_telegram_sticker",
			Result:     map[string]any{"structuredContent": map[string]any{"ok": true}},
		}),
	}

	projected := projectDiscussToolHistory(messages)
	if len(projected) != 1 {
		t.Fatalf("projected messages = %#v", projected)
	}
	text, ok := projected[0].Content[0].(sdk.TextPart)
	if !ok || text.Text != "晚安\n[Sent Telegram sticker S007]" {
		t.Fatalf("combined send projection = %#v", projected[0])
	}
}

func TestProjectDiscussToolHistoryKeepsFailedSendClosure(t *testing.T) {
	t.Parallel()

	messages := []sdk.Message{
		{
			Role: sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{sdk.ToolCallPart{
				ToolCallID: "send-1",
				ToolName:   "send",
				Input:      map[string]any{"text": "not delivered"},
			}},
		},
		sdk.ToolMessage(sdk.ToolResultPart{
			ToolCallID: "send-1",
			ToolName:   "send",
			Result:     map[string]any{"ok": false},
			IsError:    true,
		}),
	}

	projected := projectDiscussToolHistory(messages)
	if len(projected) != 2 {
		t.Fatalf("failed send closure was removed: %#v", projected)
	}
}

func TestProjectDiscussToolHistoryKeepsCrossTargetSendClosure(t *testing.T) {
	t.Parallel()

	messages := []sdk.Message{
		{
			Role: sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{sdk.ToolCallPart{
				ToolCallID: "send-other",
				ToolName:   "send",
				Input: map[string]any{
					"platform": "telegram",
					"target":   "another-chat",
					"text":     "private delivery elsewhere",
				},
			}},
		},
		sdk.ToolMessage(sdk.ToolResultPart{
			ToolCallID: "send-other",
			ToolName:   "send",
			Result: map[string]any{
				"ok":        true,
				"platform":  "telegram",
				"target":    "another-chat",
				"delivered": "target",
			},
		}),
	}

	projected := projectDiscussToolHistory(messages)
	if len(projected) != 2 {
		t.Fatalf("cross-target send closure was collapsed: %#v", projected)
	}
	if _, ok := projected[0].Content[0].(sdk.ToolCallPart); !ok {
		t.Fatalf("cross-target tool call was replaced: %#v", projected[0])
	}
}

func TestProjectDiscussToolHistoryDropsAllOrdinaryAssistantText(t *testing.T) {
	t.Parallel()

	messages := []sdk.Message{
		sdk.AssistantMessage("legacy private text"),
		sdk.AssistantMessage("unpublished assistant text"),
	}
	projected := projectDiscussToolHistory(messages)
	if len(projected) != 0 {
		t.Fatalf("ordinary assistant text leaked into projection: %#v", projected)
	}
}
