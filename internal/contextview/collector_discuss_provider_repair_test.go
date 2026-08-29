package contextview

import (
	"context"
	"encoding/json"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/chat/timeline"
)

func TestDiscussCollectorProviderIntentRepairsDanglingToolCalls(t *testing.T) {
	t.Parallel()

	frags := collectDiscussProviderContext(t, []timeline.ContextMessage{
		{
			Role:       "assistant",
			Content:    "calling lookup",
			RawContent: json.RawMessage(`[{"type":"tool-call","toolCallId":"call-1","toolName":"lookup","input":{"query":"answer"}}]`),
		},
		{Role: "user", Content: "next question"},
	})

	if len(frags) != 3 {
		t.Fatalf("frags = %d, want assistant call, synthetic result, and current user", len(frags))
	}
	synthetic := frags[1]
	msg := discussFragMessage(synthetic)
	if msg == nil || msg.Role != sdk.MessageRoleTool || len(msg.Content) != 1 {
		t.Fatalf("synthetic closure message = %#v, want one tool result", msg)
	}
	result, ok := msg.Content[0].(sdk.ToolResultPart)
	if !ok ||
		result.ToolCallID != "call-1" ||
		result.ToolName != "lookup" ||
		result.Result != contextfrag.ToolClosureRepairText ||
		!result.IsError {
		t.Fatalf("synthetic closure result = %#v, want interrupted call-1 lookup result", msg.Content[0])
	}
	if synthetic.Provenance.Source != contextfrag.SourceToolClosureRepair ||
		synthetic.Provenance.Collector != discussContextCollectorName {
		t.Fatalf("synthetic closure provenance = %+v, want discuss repair attribution", synthetic.Provenance)
	}
}

func TestDiscussCollectorProviderIntentDropsOrphanToolResults(t *testing.T) {
	t.Parallel()

	frags := collectDiscussProviderContext(t, []timeline.ContextMessage{
		{
			Role:       "tool",
			Content:    "debug fallback",
			RawContent: json.RawMessage(`[{"type":"tool-result","toolCallId":"call-1","toolName":"lookup","result":{"answer":42}}]`),
		},
		{Role: "user", Content: "next question"},
	})

	if len(frags) != 1 {
		t.Fatalf("frags = %d, want only the current user after orphan repair", len(frags))
	}
	msg := discussFragMessage(frags[0])
	if msg == nil {
		t.Fatal("remaining current-user fragment has no SDK message")
	}
	assertMessagesEqual(t, []sdk.Message{*msg}, []sdk.Message{sdk.UserMessage("next question")})
}

func collectDiscussProviderContext(t *testing.T, messages []timeline.ContextMessage) []contextfrag.ContextFrag {
	t.Helper()
	frags, err := (&DiscussContextCollector{}).Collect(context.Background(), CollectRequest{
		Scope:  contextfrag.Scope{BotID: "bot-1", SessionID: "s1"},
		Intent: contextfrag.IntentRunConfigPreProvider,
		Config: DiscussContextConfig{
			ComposedMessages: messages,
		},
	})
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}
	return frags
}
