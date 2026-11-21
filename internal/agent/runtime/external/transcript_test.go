package external

import (
	"strings"
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

func transcriptRoles(msgs []sdk.Message) string {
	var roles []string
	for _, msg := range msgs {
		role := string(msg.Role)
		for _, part := range msg.Content {
			switch p := part.(type) {
			case sdk.ToolCallPart:
				role += ":call=" + p.ToolCallID
			case sdk.ToolResultPart:
				role += ":result=" + p.ToolCallID
			}
		}
		roles = append(roles, role)
	}
	return strings.Join(roles, " ")
}

// A runtime may finish a call after the model went on. The result still goes
// with its call, so no call is left without its result in the transcript.
func TestTranscriptFilesLateResultWithItsCall(t *testing.T) {
	b := NewTranscriptRecorder()
	b.Add(event.StreamEvent{Type: event.ToolCallStart, ToolCallID: "a", ToolName: "exec"})
	b.Add(event.StreamEvent{Type: event.TextDelta, Delta: "a is running; meanwhile"})
	b.Add(event.StreamEvent{Type: event.ToolCallStart, ToolCallID: "b", ToolName: "exec"})
	b.Add(event.StreamEvent{Type: event.ToolCallEnd, ToolCallID: "b", ToolName: "exec", Result: map[string]any{"exit_code": 0}})
	b.Add(event.StreamEvent{Type: event.ToolCallEnd, ToolCallID: "a", ToolName: "exec", Result: map[string]any{"exit_code": 1}})
	b.Add(event.StreamEvent{Type: event.ToolCallEnd, ToolCallID: "unknown", ToolName: "exec"})

	got := transcriptRoles(b.Messages(""))
	want := "assistant:call=a tool:result=a assistant:call=b tool:result=b tool:result=unknown"
	if got != want {
		t.Fatalf("transcript = %s\nwant %s", got, want)
	}
}

// An approval repeats a call that was already flushed; its result goes with
// the last message that makes the call.
func TestTranscriptFilesResultWithTheLastCopyOfItsCall(t *testing.T) {
	b := NewTranscriptRecorder()
	b.Add(event.StreamEvent{Type: event.ToolCallStart, ToolCallID: "a", ToolName: "exec"})
	b.Add(event.StreamEvent{Type: event.TextDelta, Delta: "needs approval"})
	b.Add(event.StreamEvent{Type: event.ToolApprovalRequest, ToolCallID: "a", ToolName: "exec", ApprovalID: "ap", Status: "pending"})
	b.Add(event.StreamEvent{Type: event.ToolCallEnd, ToolCallID: "a", ToolName: "exec", Result: map[string]any{"exit_code": 0}})

	msgs := b.Messages("")
	last := msgs[len(msgs)-1]
	if last.Role != sdk.MessageRoleTool || !strings.HasSuffix(transcriptRoles(msgs), "assistant:call=a tool:result=a") {
		t.Fatalf("transcript = %s", transcriptRoles(msgs))
	}
}
