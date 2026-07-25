package view

import "testing"

func richActiveRunContractScript() []UIMessageStreamEvent {
	return []UIMessageStreamEvent{
		{Type: "reasoning_delta", Delta: "I need to inspect the workspace."},
		{Type: "text_delta", Delta: "I will check the current state."},
		{
			Type:       "tool_call_start",
			ToolName:   "exec",
			ToolCallID: "call-exec",
			Input:      map[string]any{"command": "pwd"},
		},
		{
			Type:       "tool_call_progress",
			ToolName:   "exec",
			ToolCallID: "call-exec",
			Progress:   "queued",
		},
		{
			Type:       "tool_call_progress",
			ToolName:   "exec",
			ToolCallID: "call-exec",
			Progress:   map[string]any{"stdout": "/workspace\n"},
		},
		{
			Type:       "tool_call_end",
			ToolName:   "exec",
			ToolCallID: "call-exec",
			Output:     map[string]any{"structuredContent": map[string]any{"stdout": "/workspace\n"}},
		},
		{
			Type:       "tool_approval_request",
			ToolName:   "exec",
			ToolCallID: "call-approval",
			Input:      map[string]any{"command": "rm -rf build"},
			ApprovalID: "approval-1",
			ShortID:    7,
			Status:     "pending",
		},
		{
			Type:        "user_input_request",
			ToolName:    "ask_user",
			ToolCallID:  "call-ask",
			Input:       map[string]any{"questions": []any{map[string]any{"text": "Continue?", "kind": "single_select"}}},
			UserInputID: "input-1",
			ShortID:     8,
			Status:      "pending",
			Metadata: map[string]any{
				"ui_payload": map[string]any{
					"version": 2,
					"questions": []any{map[string]any{
						"id":   "q1",
						"text": "Continue?",
						"kind": "single_select",
						"options": []any{
							map[string]any{"id": "yes", "label": "Yes"},
							map[string]any{"id": "no", "label": "No"},
						},
					}},
				},
			},
		},
	}
}

func TestRuntimeContractScriptAggregatesCurrentRunUIBlocks(t *testing.T) {
	t.Parallel()

	converter := NewUIMessageStreamConverter()
	blocksByID := map[int]UIMessage{}
	for _, event := range richActiveRunContractScript() {
		for _, block := range converter.HandleEvent(event) {
			blocksByID[block.ID] = block
		}
	}

	var reasoning, text, execTool, approvalTool, askUserTool *UIMessage
	for _, block := range blocksByID {
		block := block
		switch {
		case block.Type == UIMessageReasoning:
			reasoning = &block
		case block.Type == UIMessageText:
			text = &block
		case block.Type == UIMessageTool && block.ToolCallID == "call-exec":
			execTool = &block
		case block.Type == UIMessageTool && block.ToolCallID == "call-approval":
			approvalTool = &block
		case block.Type == UIMessageTool && block.ToolCallID == "call-ask":
			askUserTool = &block
		}
	}

	if reasoning == nil || reasoning.Content != "I need to inspect the workspace." {
		t.Fatalf("reasoning block = %#v, want scripted reasoning", reasoning)
	}
	if text == nil || text.Content != "I will check the current state." {
		t.Fatalf("text block = %#v, want scripted text", text)
	}
	if execTool == nil || execTool.Name != "exec" || execTool.Running == nil || *execTool.Running {
		t.Fatalf("exec tool block = %#v, want completed exec tool", execTool)
	}
	if len(execTool.Progress) != 2 {
		t.Fatalf("exec progress = %#v, want two progress snapshots", execTool.Progress)
	}
	if approvalTool == nil || approvalTool.Approval == nil || !approvalTool.Approval.CanApprove {
		t.Fatalf("approval block = %#v, want pending approval", approvalTool)
	}
	if askUserTool == nil || askUserTool.UserInput == nil || !askUserTool.UserInput.CanRespond {
		t.Fatalf("ask_user block = %#v, want pending user input", askUserTool)
	}
	if len(askUserTool.UserInput.Questions) != 1 || askUserTool.UserInput.Questions[0].Text != "Continue?" {
		t.Fatalf("ask_user questions = %#v, want scripted question", askUserTool.UserInput.Questions)
	}
}
