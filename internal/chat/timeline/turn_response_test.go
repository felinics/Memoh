package timeline

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
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

	modelMessage, err := json.Marshal(turn.ModelMessage{
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
	// Completed reasoning must not be re-injected into later prompts.
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

	modelMessage, err := json.Marshal(turn.ModelMessage{
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

	modelMessage, err := json.Marshal(turn.ModelMessage{
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

func TestDecodeTurnResponseEntryPreservesTextProviderMetadata(t *testing.T) {
	t.Parallel()

	content, err := json.Marshal([]map[string]any{{
		"type": "text",
		"text": " the answer ",
		"providerMetadata": map[string]any{
			"google": map[string]any{"thoughtSignature": "SIG_TEXT"},
		},
	}})
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	modelMessage, err := json.Marshal(turn.ModelMessage{Role: "assistant", Content: content})
	if err != nil {
		t.Fatalf("marshal model message: %v", err)
	}

	entry, ok := DecodeTurnResponseEntry(messagepkg.Message{
		Role:    "assistant",
		Content: modelMessage,
	})
	if !ok {
		t.Fatal("expected turn response entry")
	}
	part := assertRawPart(t, entry.RawContent, "text", " the answer ", "")
	meta, ok := part["providerMetadata"].(map[string]any)
	if !ok {
		t.Fatalf("providerMetadata = %#v, want map", part["providerMetadata"])
	}
	google, ok := meta["google"].(map[string]any)
	if !ok || google["thoughtSignature"] != "SIG_TEXT" {
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
	modelMessage, err := json.Marshal(turn.ModelMessage{
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

	modelMessage, err := json.Marshal(turn.ModelMessage{
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
	modelMessage, err := json.Marshal(turn.ModelMessage{
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

func TestDecodeTurnResponseEntryKeepsOnlyInterruptedReasoning(t *testing.T) {
	t.Parallel()

	// Completed reasoning remains hidden, while an interrupted checkpoint is
	// continuation state and must survive into the next prompt.
	content, err := json.Marshal([]map[string]any{
		{"type": "reasoning", "text": "thinking out loud"},
	})
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	modelMessage, err := json.Marshal(turn.ModelMessage{
		Role:    "assistant",
		Content: content,
	})
	if err != nil {
		t.Fatalf("marshal model message: %v", err)
	}
	if _, ok := DecodeTurnResponseEntry(messagepkg.Message{
		Role:    " Assistant ",
		Content: modelMessage,
	}); ok {
		t.Fatal("expected reasoning-only message to be skipped")
	}
	interrupted := messagepkg.Message{
		Role: " Assistant ", Content: modelMessage,
		Metadata: map[string]any{messagepkg.AgentStepInterruptedMetadataKey: true},
	}
	entries := DecodeTurnResponseEntries([]messagepkg.Message{interrupted, interrupted})
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want only the latest interrupted checkpoint", len(entries))
	}
	assertRawPart(t, entries[0].RawContent, "text", messagepkg.AgentStepInterruptedReasoningPrefix+"thinking out loud", "")
}

func TestDecodeTurnResponseEntriesKeepsOpaqueInterruptedReasoning(t *testing.T) {
	t.Parallel()

	content, err := json.Marshal([]map[string]any{{
		"type":   "reasoning",
		"id":     "r1",
		"text":   "",
		"format": "anthropic-v1",
		"model":  "claude-sonnet-4-20250514",
		"providerMetadata": map[string]any{
			"anthropic": map[string]any{"redactedData": "BLOB"},
		},
	}})
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	modelMessage, err := json.Marshal(turn.ModelMessage{Role: "assistant", Content: content})
	if err != nil {
		t.Fatalf("marshal model message: %v", err)
	}
	checkpoint := messagepkg.Message{
		Role:    "assistant",
		Content: modelMessage,
		Metadata: map[string]any{
			messagepkg.AgentStepInterruptedMetadataKey: true,
		},
	}

	entries := DecodeTurnResponseEntries([]messagepkg.Message{checkpoint})
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want opaque interrupted checkpoint", len(entries))
	}
	part := assertRawPart(t, entries[0].RawContent, "reasoning", "", "")
	if part["id"] != "r1" || part["format"] != "anthropic-v1" ||
		part["model"] != "claude-sonnet-4-20250514" {
		t.Fatalf("reasoning provenance was not preserved: %#v", part)
	}
	meta, ok := part["providerMetadata"].(map[string]any)
	if !ok {
		t.Fatalf("providerMetadata = %#v, want map", part["providerMetadata"])
	}
	anthropic, ok := meta["anthropic"].(map[string]any)
	if !ok || anthropic["redactedData"] != "BLOB" {
		t.Fatalf("anthropic metadata = %#v, want redactedData", meta["anthropic"])
	}

	if _, ok := DecodeTurnResponseEntry(checkpoint); ok {
		t.Fatal("completed-history decoder retained opaque reasoning")
	}
}

func TestDecodeTurnResponseEntriesDropSupersededCheckpoint(t *testing.T) {
	t.Parallel()

	// Once a completed answer follows the checkpoint, its reasoning is history:
	// re-injecting the continuation instruction would ask the model to resume an
	// answer it already delivered, on every later turn.
	reasoning, err := json.Marshal([]map[string]any{{"type": "reasoning", "text": "thinking out loud"}})
	if err != nil {
		t.Fatalf("marshal reasoning content: %v", err)
	}
	interruptedMessage, err := json.Marshal(turn.ModelMessage{Role: "assistant", Content: reasoning})
	if err != nil {
		t.Fatalf("marshal interrupted message: %v", err)
	}
	answer, err := json.Marshal([]map[string]any{{"type": "text", "text": "done"}})
	if err != nil {
		t.Fatalf("marshal answer content: %v", err)
	}
	answerMessage, err := json.Marshal(turn.ModelMessage{Role: "assistant", Content: answer})
	if err != nil {
		t.Fatalf("marshal answer message: %v", err)
	}

	entries := DecodeTurnResponseEntries([]messagepkg.Message{
		{
			Role: "assistant", Content: interruptedMessage,
			Metadata: map[string]any{messagepkg.AgentStepInterruptedMetadataKey: true},
		},
		{Role: "assistant", Content: answerMessage},
	})
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want only the completed answer", len(entries))
	}
	assertRawPart(t, entries[0].RawContent, "text", "done", "")
}

func TestDecodeTurnResponseEntryLegacyToolCallsField(t *testing.T) {
	t.Parallel()

	// Older OpenAI envelope: Content is empty string, ToolCalls carries
	// the function-call structure.
	modelMessage, err := json.Marshal(turn.ModelMessage{
		Role:    "assistant",
		Content: json.RawMessage(`""`),
		ToolCalls: []turn.ToolCall{
			{
				ID:   "call-legacy",
				Type: "function",
				Function: turn.ToolCallFunction{
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

func TestDecodeTurnResponseEntriesInterruptedLegacyToolCallIsNotDuplicated(t *testing.T) {
	t.Parallel()

	reasoning, err := json.Marshal([]map[string]any{{
		"type": "reasoning",
		"text": "still thinking",
	}})
	if err != nil {
		t.Fatalf("marshal reasoning: %v", err)
	}
	modelMessage, err := json.Marshal(turn.ModelMessage{
		Role:    "assistant",
		Content: reasoning,
		ToolCalls: []turn.ToolCall{{
			ID:   "call-legacy",
			Type: "function",
			Function: turn.ToolCallFunction{
				Name:      "send",
				Arguments: `{"text":"hi"}`,
			},
		}},
	})
	if err != nil {
		t.Fatalf("marshal model message: %v", err)
	}

	entries := DecodeTurnResponseEntries([]messagepkg.Message{{
		Role:    "assistant",
		Content: modelMessage,
		Metadata: map[string]any{
			messagepkg.AgentStepInterruptedMetadataKey: true,
		},
	}})
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want one live checkpoint", len(entries))
	}
	var parts []map[string]any
	if err := json.Unmarshal(entries[0].RawContent, &parts); err != nil {
		t.Fatalf("unmarshal raw content: %v", err)
	}
	toolCalls := 0
	for _, part := range parts {
		if part["type"] == "tool-call" && part["toolCallId"] == "call-legacy" {
			toolCalls++
		}
	}
	if toolCalls != 1 {
		t.Fatalf("legacy tool-call parts = %d, want 1: %#v", toolCalls, parts)
	}
}

func TestDecodeTurnResponseEntryDoesNotDuplicateHybridToolCall(t *testing.T) {
	t.Parallel()

	content, err := json.Marshal([]map[string]any{{
		"type":       "tool-call",
		"toolCallId": "call-hybrid",
		"toolName":   "lookup",
		"input":      map[string]any{"query": "memoh"},
	}})
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	modelMessage, err := json.Marshal(turn.ModelMessage{
		Role:    "assistant",
		Content: content,
		ToolCalls: []turn.ToolCall{{
			ID: "call-hybrid",
			Function: turn.ToolCallFunction{
				Name:      "lookup",
				Arguments: `{"query":"memoh"}`,
			},
		}},
	})
	if err != nil {
		t.Fatalf("marshal model message: %v", err)
	}

	entry, ok := DecodeTurnResponseEntry(messagepkg.Message{Role: "assistant", Content: modelMessage})
	if !ok {
		t.Fatal("expected hybrid tool-call entry")
	}
	var parts []map[string]any
	if err := json.Unmarshal(entry.RawContent, &parts); err != nil {
		t.Fatalf("unmarshal raw content: %v", err)
	}
	toolCalls := 0
	for _, part := range parts {
		if part["type"] == "tool-call" && part["toolCallId"] == "call-hybrid" {
			toolCalls++
		}
	}
	if toolCalls != 1 {
		t.Fatalf("hybrid tool-call parts = %d, want 1: %#v", toolCalls, parts)
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
		case "text", "reasoning":
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
