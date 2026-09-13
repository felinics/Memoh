package external

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/event"
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
	if !ok || receipt.Text != "Goal set: review" || receipt.ProviderMetadata["runtime_command"] != "goal" {
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

func TestTranscriptKeepsNativeToolFailure(t *testing.T) {
	messages := TranscriptFromEvents([]event.StreamEvent{
		{Type: event.ToolCallStart, ToolCallID: "read", ToolName: "read"},
		{Type: event.ToolCallEnd, ToolCallID: "read", Status: "failed", Result: "file not found"},
	}, "")
	result := messages[1].Content[0].(sdk.ToolResultPart)
	if !result.IsError || result.Result != "file not found" {
		t.Fatalf("native failure became a successful tool result: %#v", result)
	}
}
