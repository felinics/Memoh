package timeline

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	agentdomain "github.com/memohai/memoh/domains/agent"
	messagepkg "github.com/memohai/memoh/domains/agent/chat/message"
)

func TestDecodeTurnResponseEntryUsesVisibleText(t *testing.T) {
	t.Parallel()

	content, err := json.Marshal([]map[string]any{
		{"type": "reasoning", "text": "thinking"},
		{"type": "text", "text": "任务完成"},
	})
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}

	modelMessage, err := json.Marshal(agentdomain.ModelMessage{
		Role:    "assistant",
		Content: content,
	})
	if err != nil {
		t.Fatalf("marshal model message: %v", err)
	}

	entry, ok := DecodeTurnResponseEntry(messagepkg.Message{
		Role:      "assistant",
		Content:   modelMessage,
		CreatedAt: time.Unix(1710000000, 0).UTC(),
	})
	if !ok {
		t.Fatal("expected turn response entry")
	}
	if entry.Content != "任务完成" {
		t.Fatalf("content = %q, want %q", entry.Content, "任务完成")
	}
	// Reasoning must never leak into TRs to avoid re-injection into prompts.
	if strings.Contains(entry.Content, "thinking") {
		t.Fatalf("reasoning leaked into TR: %q", entry.Content)
	}
	assertRawPart(t, entry.RawContent, "text", "任务完成", "")
}

func TestDecodeTurnResponseEntryPreservesToolCallOnlyPayload(t *testing.T) {
	t.Parallel()

	content, err := json.Marshal([]map[string]any{
		{"type": "reasoning", "text": "thinking"},
		{
			"type":       "tool-call",
			"toolName":   "read",
			"toolCallId": "call-1",
			"input":      map[string]any{"path": "/tmp/a.txt"},
		},
	})
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}

	modelMessage, err := json.Marshal(agentdomain.ModelMessage{
		Role:    "assistant",
		Content: content,
	})
	if err != nil {
		t.Fatalf("marshal model message: %v", err)
	}

	entry, ok := DecodeTurnResponseEntry(messagepkg.Message{
		Role:      "assistant",
		Content:   modelMessage,
		CreatedAt: time.Unix(1710000000, 0).UTC(),
	})
	if !ok {
		t.Fatal("expected tool-call-only payload to be preserved as TR")
	}
	if strings.Contains(entry.Content, "thinking") {
		t.Fatalf("reasoning leaked: %q", entry.Content)
	}
	part := assertRawPart(t, entry.RawContent, "tool-call", "read", "call-1")
	input, ok := part["input"].(map[string]any)
	if !ok || input["path"] != "/tmp/a.txt" {
		t.Fatalf("tool input missing: %#v", part["input"])
	}
}

func TestDecodeTurnResponseEntryPreservesToolCallProviderMetadata(t *testing.T) {
	t.Parallel()

	content, err := json.Marshal([]map[string]any{
		{
			"type":       "tool-call",
			"toolName":   "read",
			"toolCallId": "call-1",
			"input":      map[string]any{"path": "/tmp/a.txt"},
			"providerMetadata": map[string]any{
				"google": map[string]any{"thoughtSignature": "sig-1"},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}

	modelMessage, err := json.Marshal(agentdomain.ModelMessage{
		Role:    "assistant",
		Content: content,
	})
	if err != nil {
		t.Fatalf("marshal model message: %v", err)
	}

	entry, ok := DecodeTurnResponseEntry(messagepkg.Message{
		Role:      "assistant",
		Content:   modelMessage,
		CreatedAt: time.Unix(1710000000, 0).UTC(),
	})
	if !ok {
		t.Fatal("expected turn response entry")
	}
	part := assertRawPart(t, entry.RawContent, "tool-call", "read", "call-1")
	meta, ok := part["providerMetadata"].(map[string]any)
	if !ok {
		t.Fatalf("providerMetadata = %#v, want map", part["providerMetadata"])
	}
	google, ok := meta["google"].(map[string]any)
	if !ok || google["thoughtSignature"] != "sig-1" {
		t.Fatalf("google metadata = %#v, want thought signature", meta["google"])
	}
}

func TestDecodeTurnResponseEntryRendersTextAndToolCall(t *testing.T) {
	t.Parallel()

	content, err := json.Marshal([]map[string]any{
		{"type": "text", "text": "Let me check."},
		{
			"type":       "tool-call",
			"toolName":   "web_search",
			"toolCallId": "call-42",
			"input":      map[string]any{"query": "today news"},
		},
	})
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	modelMessage, err := json.Marshal(agentdomain.ModelMessage{
		Role:    "assistant",
		Content: content,
	})
	if err != nil {
		t.Fatalf("marshal model message: %v", err)
	}

	entry, ok := DecodeTurnResponseEntry(messagepkg.Message{
		Role:    "assistant",
		Content: modelMessage,
	})
	if !ok {
		t.Fatal("expected entry")
	}
	if !strings.Contains(entry.Content, "Let me check.") {
		t.Fatalf("missing text portion: %q", entry.Content)
	}
	assertRawPart(t, entry.RawContent, "text", "Let me check.", "")
	assertRawPart(t, entry.RawContent, "tool-call", "web_search", "call-42")
}

func TestDecodeTurnResponseEntryToolRoleWithPartsResult(t *testing.T) {
	t.Parallel()

	content, err := json.Marshal([]map[string]any{
		{
			"type":       "tool-result",
			"toolCallId": "call-1",
			"toolName":   "web_search",
			"output": map[string]any{
				"count":   3,
				"summary": "ok",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}

	modelMessage, err := json.Marshal(agentdomain.ModelMessage{
		Role:    "tool",
		Content: content,
	})
	if err != nil {
		t.Fatalf("marshal model message: %v", err)
	}

	entry, ok := DecodeTurnResponseEntry(messagepkg.Message{
		Role:    "tool",
		Content: modelMessage,
	})
	if !ok {
		t.Fatal("expected tool role entry")
	}
	part := assertRawPart(t, entry.RawContent, "tool-result", "web_search", "call-1")
	result, ok := part["result"].(map[string]any)
	if !ok || result["count"] != float64(3) || result["summary"] != "ok" {
		t.Fatalf("structured tool output not preserved: %#v", part["result"])
	}
}

func TestDecodeTurnResponseEntryToolRoleLegacyEnvelope(t *testing.T) {
	t.Parallel()

	// Old OpenAI-style: role=tool + ToolCallID on the envelope, Content is
	// a JSON string carrying the result directly.
	resultBody := json.RawMessage(`{"status":"ok"}`)
	modelMessage, err := json.Marshal(agentdomain.ModelMessage{
		Role:       "tool",
		ToolCallID: "call-99",
		Name:       "ping",
		Content:    resultBody,
	})
	if err != nil {
		t.Fatalf("marshal model message: %v", err)
	}

	entry, ok := DecodeTurnResponseEntry(messagepkg.Message{
		Role:    "tool",
		Content: modelMessage,
	})
	if !ok {
		t.Fatal("expected entry for legacy tool envelope")
	}
	part := assertRawPart(t, entry.RawContent, "tool-result", "ping", "call-99")
	result, ok := part["result"].(map[string]any)
	if !ok || result["status"] != "ok" {
		t.Fatalf("legacy tool body missing: %#v", part["result"])
	}
}

func TestDecodeTurnResponseEntrySkipsEmpty(t *testing.T) {
	t.Parallel()

	// Only reasoning → nothing to expose to future prompts → skip.
	content, err := json.Marshal([]map[string]any{
		{"type": "reasoning", "text": "thinking out loud"},
	})
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	modelMessage, err := json.Marshal(agentdomain.ModelMessage{
		Role:    "assistant",
		Content: content,
	})
	if err != nil {
		t.Fatalf("marshal model message: %v", err)
	}
	if _, ok := DecodeTurnResponseEntry(messagepkg.Message{
		Role:    "assistant",
		Content: modelMessage,
	}); ok {
		t.Fatal("expected reasoning-only message to be skipped")
	}
}

func TestDecodeTurnResponseEntryLegacyToolCallsField(t *testing.T) {
	t.Parallel()

	// Older OpenAI envelope: Content is empty string, ToolCalls carries
	// the function-call structure.
	modelMessage, err := json.Marshal(agentdomain.ModelMessage{
		Role:    "assistant",
		Content: json.RawMessage(`""`),
		ToolCalls: []agentdomain.ToolCall{
			{
				ID:   "call-legacy",
				Type: "function",
				Function: agentdomain.ToolCallFunction{
					Name:      "send",
					Arguments: `{"text":"hi"}`,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal model message: %v", err)
	}
	entry, ok := DecodeTurnResponseEntry(messagepkg.Message{
		Role:    "assistant",
		Content: modelMessage,
	})
	if !ok {
		t.Fatal("expected legacy tool-calls envelope to decode")
	}
	part := assertRawPart(t, entry.RawContent, "tool-call", "send", "call-legacy")
	input, ok := part["input"].(map[string]any)
	if !ok || input["text"] != "hi" {
		t.Fatalf("arguments missing: %#v", part["input"])
	}
}

func assertRawPart(t *testing.T, raw json.RawMessage, partType, nameOrText, callID string) map[string]any {
	t.Helper()
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("unmarshal raw content: %v; raw=%s", err, raw)
	}
	for _, part := range parts {
		if part["type"] != partType {
			continue
		}
		switch partType {
		case "text":
			if part["text"] == nameOrText {
				return part
			}
		case "tool-call", "tool-result":
			if part["toolName"] == nameOrText && part["toolCallId"] == callID {
				return part
			}
		}
	}
	t.Fatalf("missing %s part name/text=%q callID=%q in %#v", partType, nameOrText, callID, parts)
	return nil
}
