package historyfrag

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/turn"
)

func TestStoredModelMessageCodecPreservesEnvelopeAndStructuredContent(t *testing.T) {
	t.Parallel()

	want := turn.ModelMessage{
		Role:       "assistant",
		Content:    json.RawMessage(`[{"type":"reasoning","text":"thinking"},{"type":"text","text":"done"}]`),
		Usage:      json.RawMessage(`{"inputTokens":9}`),
		ToolCallID: "legacy-call",
		Name:       "lookup",
		ToolCalls: []turn.ToolCall{{
			ID:   "call-1",
			Type: "function",
			Function: turn.ToolCallFunction{
				Name:      "lookup",
				Arguments: `{"q":"memoh"}`,
			},
		}},
	}

	raw, err := MarshalStoredModelMessage(want)
	if err != nil {
		t.Fatalf("MarshalStoredModelMessage: %v", err)
	}
	legacyRaw := mustPersistenceJSON(t, want)
	if string(raw) != string(legacyRaw) {
		t.Fatalf("stored JSON changed:\ngot  %s\nwant %s", raw, legacyRaw)
	}
	got := DecodeStoredModelMessage(nil, "row-1", "assistant", raw)
	want.Usage = nil // Usage is stored in its own database column.
	if string(mustPersistenceJSON(t, got)) != string(mustPersistenceJSON(t, want)) {
		t.Fatalf("decoded message = %s, want %s", mustPersistenceJSON(t, got), mustPersistenceJSON(t, want))
	}
}

func TestDecodeStoredModelMessageKeepsInvalidRawContent(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`not-json`)
	got := DecodeStoredModelMessage(nil, "row-raw", " assistant ", raw)
	if got.Role != "assistant" {
		t.Fatalf("role = %q, want assistant", got.Role)
	}
	if string(got.Content) != string(raw) {
		t.Fatalf("content = %s, want raw fallback %s", got.Content, raw)
	}
	got.Content[0] = 'N'
	if string(raw) != "not-json" {
		t.Fatalf("raw input was mutated: %s", raw)
	}
}

func TestDecodeStoredModelMessageSupportsLegacyShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		role       string
		wantText   string
		wantCallID string
	}{
		{name: "bare content part", raw: `{"type":"text","text":"hello"}`, role: "user", wantText: "hello"},
		{name: "legacy tool envelope", raw: `{"role":"tool","tool_call_id":"call-1","content":"ok"}`, role: "tool", wantText: "ok", wantCallID: "call-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecodeStoredModelMessage(nil, "row-legacy", tt.role, json.RawMessage(tt.raw))
			if got.TextContent() != tt.wantText {
				t.Fatalf("TextContent() = %q, want %q", got.TextContent(), tt.wantText)
			}
			if got.ToolCallID != tt.wantCallID {
				t.Fatalf("ToolCallID = %q, want %q", got.ToolCallID, tt.wantCallID)
			}
		})
	}
}

func TestStoredModelMessageToSDKMessageRestoresLegacyToolResultFields(t *testing.T) {
	t.Parallel()

	got := StoredModelMessageToSDKMessage(turn.ModelMessage{
		Role:       "tool",
		Content:    mustPersistenceJSON(t, map[string]any{"status": "ok"}),
		ToolCallID: "legacy-call-id",
		Name:       "legacy-tool",
	})
	want := sdk.Message{
		Role: sdk.MessageRoleTool,
		Content: []sdk.MessagePart{sdk.ToolResultPart{
			ToolCallID: "legacy-call-id",
			ToolName:   "legacy-tool",
			Result:     map[string]any{"status": "ok"},
		}},
	}
	assertPersistenceJSON(t, got, want)
}

func TestStoredModelMessageToSDKMessageRestoresLegacyToolCalls(t *testing.T) {
	t.Parallel()

	got := StoredModelMessageToSDKMessage(turn.ModelMessage{
		Role:    "assistant",
		Content: json.RawMessage(`""`),
		ToolCalls: []turn.ToolCall{{
			ID: "legacy-call",
			Function: turn.ToolCallFunction{
				Name:      "lookup",
				Arguments: `{"query":"memoh"}`,
			},
		}},
	})
	want := sdk.Message{
		Role: sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{sdk.ToolCallPart{
			ToolCallID: "legacy-call",
			ToolName:   "lookup",
			Input:      map[string]any{"query": "memoh"},
		}},
	}
	assertPersistenceJSON(t, got, want)
}

func TestStoredModelMessageToSDKMessageDoesNotDuplicateModernToolParts(t *testing.T) {
	t.Parallel()

	got := StoredModelMessageToSDKMessage(turn.ModelMessage{
		Role:    "assistant",
		Content: mustPersistenceJSON(t, []map[string]any{{"type": "tool-call", "toolCallId": "call-1", "toolName": "lookup", "input": map[string]any{"q": "memoh"}}}),
		ToolCalls: []turn.ToolCall{{
			ID: "call-1", Function: turn.ToolCallFunction{Name: "lookup", Arguments: `{"q":"memoh"}`},
		}},
	})
	if len(got.Content) != 1 {
		t.Fatalf("content parts = %d, want 1: %#v", len(got.Content), got.Content)
	}
}

func TestStoredModelMessageToSDKMessageKeepsModernToolResult(t *testing.T) {
	t.Parallel()

	got := StoredModelMessageToSDKMessage(turn.ModelMessage{
		Role:       "tool",
		Content:    mustPersistenceJSON(t, []map[string]any{{"type": "tool-result", "toolCallId": "call-1", "toolName": "lookup", "result": "ok"}}),
		ToolCallID: "legacy-call",
		Name:       "legacy-lookup",
	})
	if len(got.Content) != 1 {
		t.Fatalf("content parts = %d, want 1: %#v", len(got.Content), got.Content)
	}
	assertPersistenceJSON(t, got.Content[0], sdk.ToolResultPart{ToolCallID: "call-1", ToolName: "lookup", Result: "ok"})
}

func TestDecodeStoredModelMessageSupportsPreviousSDKEnvelope(t *testing.T) {
	t.Parallel()

	previous := sdk.Message{
		Role: sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{sdk.ToolCallPart{
			ToolCallID: "call-old-sdk",
			ToolName:   "lookup",
			Input:      map[string]any{"query": "memoh"},
		}},
		Usage: &sdk.Usage{InputTokens: 7, OutputTokens: 3},
	}
	raw := mustPersistenceJSON(t, previous)

	stored := DecodeStoredModelMessage(nil, "row-old-sdk", "assistant", raw)
	if stored.Usage != nil {
		t.Fatalf("embedded SDK usage should remain ignored; usage belongs to the message column: %s", stored.Usage)
	}
	assertPersistenceJSON(t, StoredModelMessageToSDKMessage(stored), sdk.Message{
		Role:    previous.Role,
		Content: previous.Content,
	})
}

func TestMarshalStoredSDKMessageRedactsPointerFilePartWithoutMutatingInput(t *testing.T) {
	t.Parallel()

	file := &sdk.FilePart{
		Data:      "secret-file-bytes",
		Filename:  "report.pdf",
		MediaType: "application/pdf",
	}
	message := sdk.UserMessage("inspect", file, sdk.ImagePart{
		Image:     "data:image/png;base64,abc",
		MediaType: "image/png",
	})

	raw, err := MarshalStoredSDKMessage(message)
	if err != nil {
		t.Fatalf("MarshalStoredSDKMessage: %v", err)
	}
	if strings.Contains(string(raw), file.Data) {
		t.Fatalf("stored payload contains file bytes: %s", raw)
	}
	if !strings.Contains(string(raw), file.Filename) {
		t.Fatalf("stored payload lost the attachment placeholder: %s", raw)
	}
	if file.Data != "secret-file-bytes" {
		t.Fatalf("input file part was mutated: %#v", file)
	}

	replayed := StoredModelMessageToSDKMessage(DecodeStoredModelMessage(nil, "row-file", "user", raw))
	if len(replayed.Content) != 3 {
		t.Fatalf("replayed content parts = %d, want text, placeholder, and image: %#v", len(replayed.Content), replayed.Content)
	}
	if _, ok := replayed.Content[1].(sdk.TextPart); !ok {
		t.Fatalf("replayed file part = %T, want redacted TextPart", replayed.Content[1])
	}
	if _, ok := replayed.Content[2].(sdk.ImagePart); !ok {
		t.Fatalf("replayed sibling part = %T, want ImagePart", replayed.Content[2])
	}
}

func mustPersistenceJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %T: %v", value, err)
	}
	return raw
}

func assertPersistenceJSON(t *testing.T, got, want any) {
	t.Helper()
	gotRaw := mustPersistenceJSON(t, got)
	wantRaw := mustPersistenceJSON(t, want)
	if string(gotRaw) != string(wantRaw) {
		t.Fatalf("JSON mismatch:\ngot  %s\nwant %s", gotRaw, wantRaw)
	}
}
