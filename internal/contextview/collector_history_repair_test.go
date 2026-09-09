package contextview

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestHistoryCollectorRepairsDanglingToolCalls(t *testing.T) {
	t.Parallel()

	messages := []sdk.Message{
		sdk.UserMessage("question"),
		assistantToolCallMessage("call-lost", "web_search", ""),
		sdk.UserMessage("next question"),
	}
	frags, err := (&HistoryMessagesCollector{}).Collect(context.Background(), CollectRequest{
		Scope:  contextfrag.Scope{BotID: "bot-1"},
		Intent: contextfrag.IntentRunConfigPreProvider,
		Config: HistoryMessagesConfig{Messages: messages, RepairToolClosures: true},
	})
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}
	if len(frags) != 4 {
		t.Fatalf("frags = %d, want synthetic closure inserted", len(frags))
	}
	synthetic := frags[2]
	msg := discussFragMessage(synthetic)
	if msg == nil || msg.Role != sdk.MessageRoleTool {
		t.Fatalf("frag after dangling call must be a synthetic tool result: %#v", synthetic)
	}
	result, ok := msg.Content[0].(sdk.ToolResultPart)
	if !ok || result.ToolCallID != "call-lost" {
		t.Fatalf("synthetic result must close the dangling call: %#v", msg.Content[0])
	}
	if !strings.Contains(synthetic.Provenance.Source, "repair") {
		t.Fatalf("synthetic closure must be attributed to the repair policy: %+v", synthetic.Provenance)
	}
}

func TestHistoryCollectorDropsOrphanToolResults(t *testing.T) {
	t.Parallel()

	messages := []sdk.Message{
		toolResultMessage("call-unknown", "web_search", "orphan"),
		sdk.UserMessage("question"),
	}
	frags, err := (&HistoryMessagesCollector{}).Collect(context.Background(), CollectRequest{
		Scope:  contextfrag.Scope{BotID: "bot-1"},
		Intent: contextfrag.IntentRunConfigPreProvider,
		Config: HistoryMessagesConfig{Messages: messages, RepairToolClosures: true},
	})
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}
	for _, frag := range frags {
		msg := discussFragMessage(frag)
		if msg != nil && msg.Role == sdk.MessageRoleTool {
			t.Fatalf("orphan tool result must not survive repair: %#v", frag)
		}
	}
}

func assistantToolCallMessage(callID, toolName, text string) sdk.Message {
	parts := make([]sdk.MessagePart, 0, 2)
	if text != "" {
		parts = append(parts, sdk.TextPart{Text: text})
	}
	parts = append(parts, sdk.ToolCallPart{
		ToolCallID: callID,
		ToolName:   toolName,
		Input:      map[string]any{},
	})
	return sdk.Message{Role: sdk.MessageRoleAssistant, Content: parts}
}

func toolResultMessage(callID, toolName, value string) sdk.Message {
	return sdk.Message{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{
		sdk.ToolResultPart{ToolCallID: callID, ToolName: toolName, Result: value},
	}}
}

func discussFragMessage(frag contextfrag.ContextFrag) *sdk.Message {
	return contextfrag.FragMessage(frag)
}
