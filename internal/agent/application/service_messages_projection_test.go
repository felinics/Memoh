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

func TestProjectDiscussToolHistoryKeepsMinimalSuccessfulSendClosureAndDropsPrivateText(t *testing.T) {
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
	if len(projected) != 3 {
		t.Fatalf("projected messages = %#v", projected)
	}
	call, ok := projected[0].Content[0].(sdk.ToolCallPart)
	if projected[0].Role != sdk.MessageRoleAssistant || !ok {
		t.Fatalf("send call projection = %#v", projected[0])
	}
	if call.ToolCallID != "send-1" || call.ToolName != "send" {
		t.Fatalf("send call identity = %#v", call)
	}
	input, ok := call.Input.(map[string]any)
	if !ok || input["text"] != "visible reply" {
		t.Fatalf("minimal send input = %#v", call.Input)
	}
	if _, exists := input["target"]; exists {
		t.Fatalf("current-conversation target leaked into projection: %#v", input)
	}
	result, ok := projected[1].Content[0].(sdk.ToolResultPart)
	if projected[1].Role != sdk.MessageRoleTool || !ok {
		t.Fatalf("send result projection = %#v", projected[1])
	}
	if result.ToolCallID != call.ToolCallID || result.ToolName != "send" || result.IsError {
		t.Fatalf("send result closure = %#v", result)
	}
	resultPayload, ok := result.Result.(map[string]any)
	if !ok || len(resultPayload) != 1 || resultPayload["ok"] != true {
		t.Fatalf("minimal send result = %#v", result.Result)
	}
	if projected[2].Role != sdk.MessageRoleUser {
		t.Fatalf("last role = %q", projected[2].Role)
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
	if len(projected) != 0 {
		t.Fatalf("deprecated Sticker MCP history leaked into projection: %#v", projected)
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"search_telegram_stickers", "send_telegram_sticker", "S007", "[Sent"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("projection contains deprecated Sticker history %q: %s", forbidden, encoded)
		}
	}
}

func TestProjectDiscussToolHistoryKeepsFirstPartyStickerSendAsProtocolPair(t *testing.T) {
	t.Parallel()

	messages := []sdk.Message{
		{
			Role: sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{
				sdk.ReasoningPart{Text: "choose a sticker privately"},
				sdk.ToolCallPart{
					ToolCallID: "send-sticker-1",
					ToolName:   "send",
					Input: map[string]any{
						"platform":   "telegram",
						"target":     "current-chat",
						"text":       "晚安",
						"reply_to":   "42",
						"sticker_id": "S012",
					},
				},
			},
		},
		sdk.ToolMessage(sdk.ToolResultPart{
			ToolCallID: "send-sticker-1",
			ToolName:   "send",
			Result:     map[string]any{"structuredContent": map[string]any{"ok": true}},
		}),
	}

	projected := projectDiscussToolHistory(messages)
	if len(projected) != 2 {
		t.Fatalf("projected messages = %#v", projected)
	}
	call, ok := projected[0].Content[0].(sdk.ToolCallPart)
	if !ok {
		t.Fatalf("projected call = %#v", projected[0])
	}
	input, ok := call.Input.(map[string]any)
	if !ok || input["text"] != "晚安" || input["reply_to"] != "42" || input["sticker_id"] != "S012" {
		t.Fatalf("first-party sticker send input = %#v", call.Input)
	}
	if len(input) != 3 {
		t.Fatalf("routing metadata was not minimized: %#v", input)
	}
	if _, ok := projected[1].Content[0].(sdk.ToolResultPart); !ok {
		t.Fatalf("projected result = %#v", projected[1])
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "[Sent") || strings.Contains(string(encoded), "choose a sticker privately") {
		t.Fatalf("private or synthetic prose leaked into projection: %s", encoded)
	}
}

func TestProjectDiscussToolHistoryDropsFailedDeprecatedStickerClosure(t *testing.T) {
	t.Parallel()

	messages := []sdk.Message{
		{
			Role: sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{sdk.ToolCallPart{
				ToolCallID: "legacy-sticker-1",
				ToolName:   "sticker_send_telegram_sticker",
				Input:      map[string]any{"sticker_id": "REMOVED-ID"},
			}},
		},
		sdk.ToolMessage(sdk.ToolResultPart{
			ToolCallID: "legacy-sticker-1",
			ToolName:   "sticker_send_telegram_sticker",
			Result:     map[string]any{"ok": false, "error": "unknown sticker"},
			IsError:    true,
		}),
	}

	if projected := projectDiscussToolHistory(messages); len(projected) != 0 {
		t.Fatalf("failed deprecated Sticker closure leaked into projection: %#v", projected)
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
