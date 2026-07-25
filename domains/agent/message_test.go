package agent_test

import (
	"encoding/json"
	"reflect"
	"testing"

	agentdomain "github.com/memohai/memoh/domains/agent"
)

func TestStreamEventTypeConstants(t *testing.T) {
	tests := []struct {
		got  agentdomain.StreamEventType
		want string
	}{
		{agentdomain.AgentStart, "agent_start"},
		{agentdomain.TextStart, "text_start"},
		{agentdomain.TextDelta, "text_delta"},
		{agentdomain.TextEnd, "text_end"},
		{agentdomain.ReasoningStart, "reasoning_start"},
		{agentdomain.ReasoningDelta, "reasoning_delta"},
		{agentdomain.ReasoningEnd, "reasoning_end"},
		{agentdomain.ToolCallInputStart, "tool_call_input_start"},
		{agentdomain.ToolCallStart, "tool_call_start"},
		{agentdomain.ToolCallMetadata, "tool_call_metadata"},
		{agentdomain.ToolCallProgress, "tool_call_progress"},
		{agentdomain.ToolCallEnd, "tool_call_end"},
		{agentdomain.ToolApprovalRequest, "tool_approval_request"},
		{agentdomain.UserInputRequest, "user_input_request"},
		{agentdomain.AttachmentDelta, "attachment_delta"},
		{agentdomain.Reaction, "reaction_delta"},
		{agentdomain.Speech, "speech_delta"},
		{agentdomain.AgentEnd, "agent_end"},
		{agentdomain.AgentAbort, "agent_abort"},
		{agentdomain.Retry, "retry"},
		{agentdomain.Progress, "progress"},
		{agentdomain.Error, "error"},
	}
	if len(tests) != 22 {
		t.Fatalf("expected 22 stream event constants, got %d", len(tests))
	}
	for _, tt := range tests {
		if string(tt.got) != tt.want {
			t.Errorf("constant = %q, want %q", tt.got, tt.want)
		}
	}
}

func TestStreamEventJSONRoundTripPreservesAllFields(t *testing.T) {
	in := agentdomain.StreamEvent{
		Type:           agentdomain.ToolCallEnd,
		Delta:          "delta",
		ToolName:       "exec",
		ToolCallID:     "call-1",
		ApprovalID:     "approval-1",
		UserInputID:    "input-1",
		ShortID:        7,
		Status:         "pending",
		Input:          map[string]any{"command": "pwd"},
		Metadata:       map[string]any{"k": "v"},
		Progress:       "queued",
		Result:         map[string]any{"stdout": "/workspace\n"},
		Attachments:    []agentdomain.FileAttachment{{Type: "image", Path: "/tmp/a.png", Mime: "image/png", Name: "a.png", ContentHash: "hash", Size: 12, PlatformKey: "pk", URL: "https://x", Base64: "YmFzZQ==", Metadata: map[string]any{"w": 1}}},
		Reactions:      []agentdomain.ReactionItem{{Emoji: "👍"}},
		Speeches:       []agentdomain.SpeechItem{{Text: "hello"}},
		Messages:       json.RawMessage(`[{"role":"assistant"}]`),
		Usage:          json.RawMessage(`{"totalTokens":1}`),
		Reasoning:      []string{"step-a", "step-b"},
		Error:          "boom",
		Attempt:        2,
		MaxAttempt:     5,
		RetryError:     "retryable",
		StepNumber:     3,
		TotalSteps:     9,
		ProgressStatus: "running",
	}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out agentdomain.StreamEvent
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	roundTrip, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	if !jsonEqual(t, data, roundTrip) {
		t.Fatalf("round-trip JSON mismatch\n got: %s\nwant: %s", roundTrip, data)
	}
	if out.Type != in.Type || out.TotalSteps != in.TotalSteps || !reflect.DeepEqual(out.Reasoning, in.Reasoning) {
		t.Fatalf("contract fields drifted: type=%q totalSteps=%d reasoning=%v", out.Type, out.TotalSteps, out.Reasoning)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("raw map: %v", err)
	}
	required := []string{
		"type", "delta", "toolName", "toolCallId", "approvalId", "userInputId",
		"shortId", "status", "input", "metadata", "progress", "result",
		"attachments", "reactions", "speeches", "messages", "usage",
		"reasoning", "error", "attempt", "maxAttempt", "retryError",
		"stepNumber", "totalSteps", "progressStatus",
	}
	for _, key := range required {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing JSON key %q", key)
		}
	}
	if _, ok := raw["reasoning"]; !ok {
		t.Fatal("reasoning field must remain on the wire contract")
	}
	if _, ok := raw["totalSteps"]; !ok {
		t.Fatal("totalSteps field must remain on the wire contract")
	}
}

func TestStreamEventOmitempty(t *testing.T) {
	data, err := json.Marshal(agentdomain.StreamEvent{Type: agentdomain.AgentStart})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("omitempty keys = %#v, want only type", raw)
	}
	if raw["type"] != "agent_start" {
		t.Fatalf("type = %#v, want agent_start", raw["type"])
	}
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(a, &left); err != nil {
		t.Fatalf("decode left: %v", err)
	}
	if err := json.Unmarshal(b, &right); err != nil {
		t.Fatalf("decode right: %v", err)
	}
	return reflect.DeepEqual(left, right)
}

func TestStreamEventIsTerminal(t *testing.T) {
	tests := []struct {
		typ  agentdomain.StreamEventType
		want bool
	}{
		{agentdomain.AgentEnd, true},
		{agentdomain.AgentAbort, true},
		{agentdomain.AgentStart, false},
		{agentdomain.Error, false},
		{agentdomain.TextDelta, false},
	}
	for _, tt := range tests {
		got := (agentdomain.StreamEvent{Type: tt.typ}).IsTerminal()
		if got != tt.want {
			t.Errorf("%s.IsTerminal() = %v, want %v", tt.typ, got, tt.want)
		}
	}
}
