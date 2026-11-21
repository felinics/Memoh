package view

import (
	"testing"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
)

func TestApplyToolApprovalsUsesCanApproveFunction(t *testing.T) {
	t.Parallel()

	turns := []UITurn{
		{
			Role: "assistant",
			Messages: []UIMessage{
				{
					Type:       UIMessageTool,
					ToolCallID: "call-1",
				},
			},
		},
	}
	approvals := []toolapproval.Request{
		{
			ID:         "approval-1",
			ToolCallID: "call-1",
			ShortID:    7,
			Status:     toolapproval.StatusPending,
		},
	}

	ApplyToolApprovals(turns, approvals, func(req toolapproval.Request) bool {
		return req.ID == "approval-2"
	})

	approval := turns[0].Messages[0].Approval
	if approval == nil {
		t.Fatal("approval metadata was not merged")
	}
	if approval.Status != toolapproval.StatusPending {
		t.Fatalf("approval status = %q, want pending", approval.Status)
	}
	if approval.CanApprove {
		t.Fatal("ApplyToolApprovals ignored injected canApprove function")
	}
}

func TestApplyUserInputsRestoresStoredAnswers(t *testing.T) {
	t.Parallel()

	turns := []UITurn{{
		Role: "assistant",
		Messages: []UIMessage{{
			Type:       UIMessageTool,
			ToolCallID: "call-ask",
		}},
	}}
	requests := []userinput.Request{{
		ID:         "input-1",
		ToolCallID: "call-ask",
		Status:     userinput.StatusSubmitted,
		Result: map[string]any{
			"answers": []any{map[string]any{
				"question_id": "q1",
				"question":    "Which plan?",
				"selected": []any{map[string]any{
					"id": "q1.o2", "label": "Plan B",
				}},
			}},
		},
	}}

	ApplyUserInputs(turns, requests, nil)

	merged := turns[0].Messages[0].UserInput
	if merged == nil || len(merged.Answers) != 1 || len(merged.Answers[0].Selected) != 1 {
		t.Fatalf("stored answers were not merged: %#v", merged)
	}
	if merged.Answers[0].Selected[0].Label != "Plan B" || merged.CanRespond {
		t.Fatalf("merged user input = %#v", merged)
	}
}
