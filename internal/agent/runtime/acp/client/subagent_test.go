package client

import (
	"context"
	"errors"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
)

type rejectedSubagentQuestion struct {
	userinput.FlowService
	requests int
}

func (s *rejectedSubagentQuestion) CreatePending(context.Context, userinput.CreatePendingInput) (userinput.Request, error) {
	s.requests++
	return userinput.Request{}, errors.New("test refuses to open a user question")
}

func TestSubagentElicitationCannotOpenUserQuestion(t *testing.T) {
	svc := &rejectedSubagentQuestion{}
	callbacks := &clientCallbacks{userInput: svc, baseSession: ToolSessionContext{
		BotID: "bot", SessionID: "child", RunID: "child-run", IsSubagent: true, CanRequestUserInput: true,
	}}
	response, err := callbacks.CreateElicitation(t.Context(), createElicitationRequest{Mode: "form", Message: "Value?", RequestedSchema: map[string]any{
		"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}},
	}})
	if err != nil || response.Decline == nil || svc.requests != 0 {
		t.Fatalf("child elicitation was not declined without user interaction: response=%+v requests=%d err=%v", response, svc.requests, err)
	}
}

func TestSubagentRejectsURLQuestionsButPreservesExecutionApproval(t *testing.T) {
	for _, kind := range []string{"url", "drifted-url", "exec"} {
		t.Run(kind, func(t *testing.T) {
			approval := &fakeACPToolApproval{decision: toolapproval.Request{Status: toolapproval.StatusApproved, DecidedByUser: true}}
			callbacks := &clientCallbacks{root: "/data", approval: approval, events: &toolEventEmitter{}, baseSession: ToolSessionContext{
				BotID: "bot", SessionID: "child", RunID: "child-run", IsSubagent: true, CanRequestUserInput: true,
			}}
			callbacks.setPromptState(newEventCollector(), nil, callbacks.baseSession)
			call := acp.ToolCallUpdate{ToolCallId: "elicitation-url", Title: acp.Ptr("Open this URL"), Kind: acp.Ptr(acp.ToolKindOther), RawInput: map[string]any{
				"serverName": "external", "url": "https://example.com/confirm",
			}}
			switch kind {
			case "drifted-url":
				call.ToolCallId = "custom-confirm"
			case "exec":
				call = acp.ToolCallUpdate{ToolCallId: "exec", Title: acp.Ptr("Run command"), Kind: acp.Ptr(acp.ToolKindExecute), RawInput: map[string]any{"command": "ls"}}
			}
			response, err := callbacks.RequestPermission(t.Context(), acp.RequestPermissionRequest{ToolCall: call, Options: []acp.PermissionOption{
				{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: "allow"},
				{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: "reject"},
			}})
			if err != nil || response.Outcome.Selected == nil {
				t.Fatalf("permission failed: response=%+v err=%v", response, err)
			}
			want, count := acp.PermissionOptionId("reject"), 0
			if kind == "exec" {
				want, count = "allow", 1
			}
			if response.Outcome.Selected.OptionId != want || approval.createdCount() != count {
				t.Fatalf("wrong child decision boundary: response=%+v approvals=%d", response, approval.createdCount())
			}
		})
	}
}
