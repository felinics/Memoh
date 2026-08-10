package client

import (
	"fmt"
	"strings"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	userinput "github.com/memohai/memoh/internal/agent/decision/input"
	"github.com/memohai/memoh/internal/agent/event"
)

// TestTranscriptSurvivesEventBufferCap ensures transcript persistence is not
// limited by the capped UI event buffer.
func TestTranscriptSurvivesEventBufferCap(t *testing.T) {
	t.Parallel()

	collector := newEventCollector()
	total := maxCollectedStreamEvents + 100
	for i := 0; i < total; i++ {
		collector.record(event.StreamEvent{Type: event.TextDelta, Delta: fmt.Sprintf("[%d]", i)})
	}
	result := collector.result()

	if len(result.Events) != maxCollectedStreamEvents {
		t.Fatalf("UI event buffer = %d events, want capped at %d", len(result.Events), maxCollectedStreamEvents)
	}
	if len(result.Output) != 1 {
		t.Fatalf("transcript = %d messages, want 1 assistant message", len(result.Output))
	}
	text, ok := result.Output[0].Content[0].(sdk.TextPart)
	if !ok {
		t.Fatalf("transcript content = %#v, want text part", result.Output[0].Content[0])
	}
	if !strings.HasPrefix(text.Text, "[0]") {
		t.Fatalf("transcript lost its beginning: starts with %q", text.Text[:20])
	}
	if !strings.HasSuffix(text.Text, fmt.Sprintf("[%d]", total-1)) {
		t.Fatalf("transcript lost its end")
	}
}

// TestAppendTranscriptTextMergeSemantics pins the failure-note semantics:
// merge into a trailing assistant text when possible, otherwise a new
// assistant message - mirroring how the builder accumulates text while
// streaming.
func TestAppendTranscriptTextMergeSemantics(t *testing.T) {
	t.Parallel()

	merged := AppendTranscriptText([]sdk.Message{
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: "partial answer"}}},
	}, "agent failed: boom")
	if len(merged) != 1 {
		t.Fatalf("messages = %d, want merged into existing assistant", len(merged))
	}
	if text := merged[0].Content[0].(sdk.TextPart).Text; text != "partial answer\n\nagent failed: boom" {
		t.Fatalf("merged text = %q", text)
	}

	appended := AppendTranscriptText([]sdk.Message{
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ToolCallPart{ToolCallID: "c1", ToolName: "exec"}}},
	}, "agent failed: boom")
	if len(appended) != 2 {
		t.Fatalf("messages = %d, want new assistant after tool-call message", len(appended))
	}

	fromEmpty := AppendTranscriptText(nil, "agent failed: boom")
	if len(fromEmpty) != 1 || fromEmpty[0].Role != sdk.MessageRoleAssistant {
		t.Fatalf("fromEmpty = %#v, want single assistant message", fromEmpty)
	}
}

func TestTranscriptMCPErrorResultSetsToolResultError(t *testing.T) {
	t.Parallel()

	recorder := NewTranscriptRecorder()
	recorder.Add(event.StreamEvent{
		Type:       event.ToolCallStart,
		ToolCallID: "call-1",
		ToolName:   "write",
		Input:      map[string]any{"path": "notes.txt"},
	})
	recorder.Add(event.StreamEvent{
		Type:       event.ToolCallEnd,
		ToolCallID: "call-1",
		ToolName:   "write",
		Result: map[string]any{
			"isError": true,
			"content": []map[string]any{{
				"type": "text",
				"text": "tool execution was not approved",
			}},
		},
	})

	messages := recorder.Messages("")
	if len(messages) != 2 {
		t.Fatalf("messages = %#v, want assistant tool call and tool result", messages)
	}
	result, ok := messages[1].Content[0].(sdk.ToolResultPart)
	if !ok {
		t.Fatalf("message[1] = %#v, want tool result", messages[1])
	}
	if !result.IsError {
		t.Fatalf("tool result IsError = false, want true for MCP isError result")
	}
}

func TestTranscriptKeepsSubmittedUserInputAnswers(t *testing.T) {
	t.Parallel()

	recorder := NewTranscriptRecorder()
	recorder.Add(event.StreamEvent{
		Type:       event.ToolCallStart,
		ToolCallID: "call-ask",
		ToolName:   "ask_user",
	})
	recorder.Add(event.StreamEvent{
		Type:        event.UserInputRequest,
		ToolCallID:  "call-ask",
		ToolName:    "ask_user",
		UserInputID: "input-1",
		Status:      userinput.StatusSubmitted,
		Metadata: map[string]any{
			"answers": []userinput.UIAnswer{{
				QuestionID: "q1",
				Question:   "Continue?",
				Selected:   []userinput.UIOption{{ID: "yes", Label: "Yes"}},
			}},
		},
	})

	messages := recorder.Messages("")
	toolCall := messages[0].Content[0].(sdk.ToolCallPart)
	metadata := toolCall.ProviderMetadata["user_input"].(map[string]any)
	answers := metadata["answers"].([]userinput.UIAnswer)
	if len(answers) != 1 || answers[0].Selected[0].Label != "Yes" {
		t.Fatalf("submitted answers = %#v", answers)
	}
}

func TestTranscriptLimitsToolResultOutput(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("tool output ", 300) + "\nTAIL"
	recorder := NewTranscriptRecorder(ToolOutputLimit{MaxBytes: 512, MaxLines: 80})
	recorder.Add(event.StreamEvent{
		Type:       event.ToolCallStart,
		ToolCallID: "call-1",
		ToolName:   "exec",
		Input:      map[string]any{"command": "test"},
	})
	recorder.Add(event.StreamEvent{
		Type:       event.ToolCallEnd,
		ToolCallID: "call-1",
		ToolName:   "exec",
		Result:     map[string]any{"stdout": large},
	})

	messages := recorder.Messages("")
	result, ok := messages[1].Content[0].(sdk.ToolResultPart)
	if !ok {
		t.Fatalf("message[1] = %#v, want tool result", messages[1])
	}
	output, ok := result.Result.(map[string]any)
	if !ok {
		t.Fatalf("tool result = %#v, want map", result.Result)
	}
	stdout, ok := output["stdout"].(string)
	if !ok {
		t.Fatalf("stdout = %#v, want string", output["stdout"])
	}
	if len(stdout) >= len(large) {
		t.Fatalf("stdout was not limited")
	}
	for _, want := range []string{"[memoh pruned]", "HEAD", "TAIL"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestEventCollectorLimitsToolResultEvents(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("tool output ", 300) + "\nTAIL"
	collector := newEventCollector(ToolOutputLimit{MaxBytes: 512, MaxLines: 80})
	collector.record(event.StreamEvent{
		Type:       event.ToolCallEnd,
		ToolCallID: "call-1",
		ToolName:   "exec",
		Result:     map[string]any{"stdout": large},
	})

	result := collector.result()
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	output, ok := result.Events[0].Result.(map[string]any)
	if !ok {
		t.Fatalf("event result = %#v, want map", result.Events[0].Result)
	}
	stdout, ok := output["stdout"].(string)
	if !ok {
		t.Fatalf("stdout = %#v, want string", output["stdout"])
	}
	if len(stdout) >= len(large) || !strings.Contains(stdout, "[memoh pruned]") {
		t.Fatalf("event stdout was not limited:\n%s", stdout)
	}
}
