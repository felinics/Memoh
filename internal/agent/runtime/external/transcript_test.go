package external

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/partmeta"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

// Some agents deliver the reply only as final text. A command receipt earlier
// in the turn is not that reply and must not suppress the fallback.
func TestTranscriptCommandOutputKeepsFallbackText(t *testing.T) {
	messages := TranscriptFromEvents([]event.StreamEvent{
		{Type: event.CommandOutput, ToolName: "goal", Delta: "Goal set: review"},
	}, "I will start.")
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want the receipt and the reply", len(messages))
	}
	receipt, ok := messages[0].Content[0].(sdk.TextPart)
	if !ok || receipt.Text != "Goal set: review" || partmeta.Unfold(receipt.ProviderMetadata)["runtime_command"] != "goal" {
		t.Fatalf("receipt = %#v", messages[0].Content[0])
	}
	reply, ok := messages[1].Content[0].(sdk.TextPart)
	if !ok || reply.Text != "I will start." || reply.ProviderMetadata != nil {
		t.Fatalf("reply = %#v", messages[1].Content[0])
	}

	// Streamed deltas still take precedence over the fallback.
	messages = TranscriptFromEvents([]event.StreamEvent{
		{Type: event.CommandOutput, ToolName: "goal", Delta: "Goal set: review"},
		{Type: event.TextDelta, Delta: "Reviewed."},
	}, "Reviewed.")
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want the receipt and the streamed reply", len(messages))
	}
	if reply, ok := messages[1].Content[0].(sdk.TextPart); !ok || reply.Text != "Reviewed." {
		t.Fatalf("reply = %#v", messages[1].Content[0])
	}
}

func TestTranscriptKeepsReasoningBlocksSeparate(t *testing.T) {
	messages := TranscriptFromEvents([]event.StreamEvent{
		{Type: event.ReasoningStart},
		{Type: event.ReasoningDelta, Delta: "A"},
		{Type: event.ReasoningEnd},
		{Type: event.ReasoningStart},
		{Type: event.ReasoningDelta, Delta: "B"},
		{Type: event.ReasoningEnd},
		{Type: event.TextDelta, Delta: "answer"},
	}, "")
	if len(messages) != 1 || len(messages[0].Content) != 3 {
		t.Fatalf("messages = %#v", messages)
	}
	first, _ := messages[0].Content[0].(sdk.ReasoningPart)
	second, _ := messages[0].Content[1].(sdk.ReasoningPart)
	if first.Text != "A" || second.Text != "B" {
		t.Fatalf("reasoning parts = %#v", messages[0].Content)
	}
}

func TestTranscriptKeepsNativeToolFailure(t *testing.T) {
	messages := TranscriptFromEvents([]event.StreamEvent{
		{Type: event.ToolCallStart, ToolCallID: "read", ToolName: "read"},
		{Type: event.ToolCallEnd, ToolCallID: "read", Status: "failed", Result: "file not found"},
	}, "")
	result := messages[1].Content[0].(sdk.ToolResultPart)
	if !result.IsError || toolexec.OutputValue(result.Result) != "file not found" {
		t.Fatalf("native failure became a successful tool result: %#v", result)
	}
}
