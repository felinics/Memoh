package application

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
)

func TestPruneMessagesForGateway_PrunesToolResultContent(t *testing.T) {
	t.Parallel()

	unit := "汉😀"
	huge := strings.Repeat(unit, (gatewayToolPayloadMaxBytes/len(unit))+20)
	msgs := []ModelMessage{
		{Role: "tool", Content: newTextContent(huge), ToolCallID: "call-1"},
	}
	out := pruneMessagesForGateway(msgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	got := out[0].TextContent()
	if strings.Contains(got, huge) {
		t.Fatalf("expected tool content to be pruned")
	}
	if !strings.Contains(got, gatewayToolPayloadPrunedMarker) {
		t.Fatalf("expected pruned marker, got: %q", got[:minLen(len(got), 80)])
	}
	if !utf8.ValidString(got) {
		t.Fatalf("expected pruned tool content to remain valid UTF-8")
	}
}

func TestPruneMessagesForGateway_PrunesToolCallArguments(t *testing.T) {
	t.Parallel()

	repeated := strings.Repeat("猫😺", (gatewayToolPayloadMaxBytes/len("猫😺"))+20)
	hugeArgs := `{"a":"` + repeated + `"}`
	msgs := []ModelMessage{
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{
					ID:   "call-1",
					Type: "function",
					Function: ToolCallFunction{
						Name:      "big_tool",
						Arguments: hugeArgs,
					},
				},
			},
		},
	}
	out := pruneMessagesForGateway(msgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if len(out[0].ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(out[0].ToolCalls))
	}
	got := out[0].ToolCalls[0].Function.Arguments
	if strings.Contains(got, repeated) {
		t.Fatalf("expected tool arguments to be pruned")
	}
	if !strings.Contains(got, gatewayToolPayloadPrunedMarker) {
		t.Fatalf("expected pruned marker in args")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("expected pruned tool arguments to remain valid UTF-8")
	}
}

func TestPruneHistoryForGateway_ClearsStaleUsageTokensAfterPrune(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("汉😀", (gatewayToolPayloadMaxBytes/len("汉😀"))+20)
	firstTokens := 123
	secondTokens := 456

	in := []historyfrag.HistoryRecord{
		historyRecord("row-1", ModelMessage{Role: "tool", Content: newTextContent(huge), ToolCallID: "call-1"}, func(record *historyfrag.HistoryRecord) {
			record.UsageInputTokens = &firstTokens
		}),
		historyRecord("row-2", ModelMessage{Role: "user", Content: newTextContent("hi")}, func(record *historyfrag.HistoryRecord) {
			record.UsageInputTokens = &secondTokens
		}),
	}

	out := pruneHistoryForGateway(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
	if out[0].UsageInputTokens != nil {
		t.Fatalf("expected first UsageInputTokens to be cleared after prune")
	}
	if out[1].UsageInputTokens != nil {
		t.Fatalf("expected subsequent UsageInputTokens to be cleared after earlier prune")
	}
}

func TestPruneHistoryForGateway_PreservesUsageTokensWhenUnchanged(t *testing.T) {
	t.Parallel()

	tokens := 321
	in := []historyfrag.HistoryRecord{
		historyRecord("row-1", ModelMessage{Role: "user", Content: newTextContent("short")}, func(record *historyfrag.HistoryRecord) {
			record.UsageInputTokens = &tokens
		}),
	}
	out := pruneHistoryForGateway(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if out[0].UsageInputTokens == nil || *out[0].UsageInputTokens != tokens {
		t.Fatalf("expected UsageInputTokens to be preserved")
	}
}

func TestPruneMessagesForGateway_ToolResultPartsRemainValidToolMessageSchema(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("a", gatewayToolPayloadMaxBytes+100)
	part := map[string]any{
		"type":       "tool-result",
		"toolCallId": "call-1",
		"toolName":   "big_tool",
		"providerOptions": map[string]any{
			"test-provider": map[string]any{"mode": "strict"},
		},
		"extraPart": "keep-part",
		"output": map[string]any{
			"type":  "text",
			"value": huge,
			"providerOptions": map[string]any{
				"test-provider": map[string]any{"cache": true},
			},
			"extraOutput": "keep-output",
		},
	}
	content, err := json.Marshal([]any{part})
	if err != nil {
		t.Fatalf("marshal tool content: %v", err)
	}
	msgs := []ModelMessage{
		{Role: "tool", Content: content, ToolCallID: "call-1"},
	}

	out := pruneMessagesForGateway(msgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if !bytes.HasPrefix(bytes.TrimSpace(out[0].Content), []byte("[")) {
		t.Fatalf("expected tool content to remain an array, got: %q", string(out[0].Content[:minLen(len(out[0].Content), 80)]))
	}
	if !bytes.Contains(out[0].Content, []byte(`"type":"tool-result"`)) {
		t.Fatalf("expected tool-result part to be preserved")
	}
	if !bytes.Contains(out[0].Content, []byte(gatewayToolPayloadPrunedMarker)) {
		t.Fatalf("expected pruned marker in tool output")
	}

	var parts []map[string]any
	if err := json.Unmarshal(out[0].Content, &parts); err != nil {
		t.Fatalf("unmarshal pruned tool content: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	if parts[0]["extraPart"] != "keep-part" {
		t.Fatalf("expected extra part field preserved")
	}
	if _, ok := parts[0]["providerOptions"]; !ok {
		t.Fatalf("expected part providerOptions preserved")
	}
	outputAny, ok := parts[0]["output"].(map[string]any)
	if !ok {
		t.Fatalf("expected output object")
	}
	if outputAny["extraOutput"] != "keep-output" {
		t.Fatalf("expected output extra field preserved")
	}
	if _, ok := outputAny["providerOptions"]; !ok {
		t.Fatalf("expected output providerOptions preserved")
	}
	if outputAny["type"] != "text" {
		t.Fatalf("expected output.type=text, got %v", outputAny["type"])
	}
	value, ok := outputAny["value"].(string)
	if !ok {
		t.Fatalf("expected output.value string")
	}
	if len(value) >= len(huge) {
		t.Fatalf("expected output.value to be pruned")
	}
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}
