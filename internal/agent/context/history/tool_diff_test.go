package historyfrag

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/partmeta"
	"github.com/felinics/memoh/internal/agent/turn"
)

func TestExtractToolCallDiffsLiftsDiffsOffContent(t *testing.T) {
	t.Parallel()

	diff := "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n+new\n"
	content, err := json.Marshal([]map[string]any{
		{"type": "text", "text": "done"},
		{
			"type":       "tool-call",
			"toolCallId": "call-1",
			"toolName":   "edit",
			"input":      map[string]any{"path": "f"},
			"providerMetadata": map[string]any{
				"diff":               diff,
				"execution_location": map[string]any{"kind": "remote"},
			},
		},
		{
			"type":             "tool-call",
			"toolCallId":       "call-2",
			"toolName":         "write",
			"providerMetadata": map[string]any{"diff": "@@ -0,0 +1 @@\n+new"},
		},
	})
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	msg := turn.ModelMessage{Role: "assistant", Content: content}

	got, diffs := ExtractToolCallDiffs(msg)

	if len(diffs) != 2 || diffs["call-1"] != diff || diffs["call-2"] != "@@ -0,0 +1 @@\n+new" {
		t.Fatalf("diffs = %#v", diffs)
	}
	serialized := string(got.Content)
	if strings.Contains(serialized, `"diff"`) {
		t.Fatalf("content still carries diff payloads: %s", serialized)
	}
	if !strings.Contains(serialized, `"execution_location"`) {
		t.Fatalf("unrelated providerMetadata was dropped: %s", serialized)
	}
	// A providerMetadata that held only the diff is removed entirely.
	var parts []map[string]any
	if err := json.Unmarshal(got.Content, &parts); err != nil {
		t.Fatalf("reunmarshal: %v", err)
	}
	if _, ok := parts[2]["providerMetadata"]; ok {
		t.Fatalf("emptied providerMetadata should be removed: %v", parts[2])
	}
	// The original message content is not mutated.
	if !strings.Contains(string(msg.Content), `"diff"`) {
		t.Fatal("input message was mutated")
	}
}

func TestExtractToolCallDiffsLeavesOtherMessagesAlone(t *testing.T) {
	t.Parallel()

	cases := map[string]turn.ModelMessage{
		"non assistant role": {
			Role:    "tool",
			Content: json.RawMessage(`[{"type":"tool-result","toolCallId":"c","providerMetadata":{"diff":"x"}}]`),
		},
		"no diff marker": {
			Role:    "assistant",
			Content: json.RawMessage(`[{"type":"tool-call","toolCallId":"c","toolName":"edit"}]`),
		},
		"unparseable content": {
			Role:    "assistant",
			Content: json.RawMessage(`"diff" is mentioned but this is not a parts array`),
		},
	}
	for name, msg := range cases {
		got, diffs := ExtractToolCallDiffs(msg)
		if diffs != nil {
			t.Fatalf("%s: diffs = %#v, want nil", name, diffs)
		}
		if string(got.Content) != string(msg.Content) {
			t.Fatalf("%s: content changed to %s", name, got.Content)
		}
	}
}

func TestExtractToolCallDiffsFromSDK(t *testing.T) {
	t.Parallel()

	msg := sdk.Message{
		Role: sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{
			sdk.ToolCallPart{
				ToolCallID: "call-1",
				ToolName:   "edit",
				ProviderMetadata: partmeta.Fold(map[string]any{
					"diff":               "@@ -1 +1 @@",
					"execution_location": map[string]any{"kind": "remote"},
				}),
			},
		},
	}

	got, diffs := ExtractToolCallDiffsFromSDK(msg)
	if diffs["call-1"] != "@@ -1 +1 @@" {
		t.Fatalf("diffs = %#v", diffs)
	}
	call, ok := got.Content[0].(sdk.ToolCallPart)
	if !ok {
		t.Fatalf("part type = %T", got.Content[0])
	}
	if partmeta.Has(call.ProviderMetadata, "diff") {
		t.Fatalf("diff still in providerMetadata: %#v", call.ProviderMetadata)
	}
	if !partmeta.Has(call.ProviderMetadata, "execution_location") {
		t.Fatalf("unrelated providerMetadata was dropped: %#v", call.ProviderMetadata)
	}
	// Original untouched.
	orig := msg.Content[0].(sdk.ToolCallPart)
	if !partmeta.Has(orig.ProviderMetadata, "diff") {
		t.Fatal("input message was mutated")
	}
}
