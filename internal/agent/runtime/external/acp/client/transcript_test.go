package client

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/partmeta"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

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
	metadata, _ := partmeta.Object(toolCall.ProviderMetadata, partmeta.KeyUserInput)
	var answers []userinput.UIAnswer
	if raw, err := json.Marshal(metadata["answers"]); err != nil || json.Unmarshal(raw, &answers) != nil {
		t.Fatalf("submitted answers did not decode: %#v", metadata["answers"])
	}
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
	output, ok := toolexec.OutputValue(result.Result).(map[string]any)
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
