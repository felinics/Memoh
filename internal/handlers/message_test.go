package handlers

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	chatview "github.com/felinics/memoh/internal/agent/view"
	session "github.com/felinics/memoh/internal/chat/thread"
)

type testFlusher struct{}

func (*testFlusher) Flush() {}

func TestParseBeforeParam(t *testing.T) {
	t.Parallel()

	if _, ok := parseBeforeParam(""); ok {
		t.Fatalf("expected empty before value to be ignored")
	}
	parsed, ok := parseBeforeParam("1735689600000")
	if !ok {
		t.Fatalf("expected epoch millis before value to parse")
	}
	if parsed.UnixMilli() != 1735689600000 {
		t.Fatalf("expected parsed epoch millis 1735689600000, got %d", parsed.UnixMilli())
	}
}

func TestIsUserFacingSessionType(t *testing.T) {
	t.Parallel()

	for _, typ := range []string{session.TypeChat, session.TypeDiscuss, session.TypeACPAgent} {
		if !session.IsUserFacingType(typ) {
			t.Fatalf("expected %q to be user-facing", typ)
		}
	}
	for _, typ := range []string{session.TypeSchedule, session.TypeSubagent, ""} {
		if session.IsUserFacingType(typ) {
			t.Fatalf("expected %q to be filtered out of user-facing surfaces", typ)
		}
	}
}

func TestMergeToolApprovalsUsesCanApproveFunction(t *testing.T) {
	t.Parallel()

	turns := []chatview.UITurn{
		{
			Role: "assistant",
			Messages: []chatview.UIMessage{
				{
					Type:       chatview.UIMessageTool,
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

	mergeToolApprovals(turns, approvals, func(req toolapproval.Request) bool {
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
		t.Fatal("mergeToolApprovals ignored injected canApprove function")
	}
}

func TestMergeUserInputsRestoresStoredAnswers(t *testing.T) {
	t.Parallel()

	turns := []chatview.UITurn{{
		Role: "assistant",
		Messages: []chatview.UIMessage{{
			Type:       chatview.UIMessageTool,
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

	mergeUserInputs(turns, requests, nil)

	merged := turns[0].Messages[0].UserInput
	if merged == nil || len(merged.Answers) != 1 || len(merged.Answers[0].Selected) != 1 {
		t.Fatalf("stored answers were not merged: %#v", merged)
	}
	if merged.Answers[0].Selected[0].Label != "Plan B" || merged.CanRespond {
		t.Fatalf("merged user input = %#v", merged)
	}
}

func TestWriteSSEJSON(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	flusher := &testFlusher{}

	if err := writeSSEJSON(&output, flusher, map[string]any{"type": "ping"}); err != nil {
		t.Fatalf("writeSSEJSON failed: %v", err)
	}
	raw := output.String()
	if !strings.HasPrefix(raw, "data: ") {
		t.Fatalf("expected SSE data prefix, got %q", raw)
	}
	if !strings.HasSuffix(raw, "\n\n") {
		t.Fatalf("expected SSE payload suffix, got %q", raw)
	}
	payloadText := strings.TrimSuffix(strings.TrimPrefix(raw, "data: "), "\n\n")
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadText), &payload); err != nil {
		t.Fatalf("decode SSE payload failed: %v", err)
	}
	if payload["type"] != "ping" {
		t.Fatalf("expected payload type ping, got %#v", payload["type"])
	}
}

// A fork copies history, not the decision ledger: a copied pending snapshot
// has no row in the new session, so it must render read-only, with the
// outcome the source session recorded.
func TestDecisionSnapshotsWithoutSessionRowsAreReadOnly(t *testing.T) {
	t.Parallel()

	turns := []chatview.UITurn{{
		Role: "assistant",
		Messages: []chatview.UIMessage{
			{
				Type:       chatview.UIMessageTool,
				ToolCallID: "call-exec",
				Approval:   &chatview.UIToolApproval{ApprovalID: "approval-src", Status: toolapproval.StatusPending, CanApprove: true},
			},
			{
				Type:       chatview.UIMessageTool,
				ToolCallID: "call-ask",
				UserInput:  &chatview.UIUserInput{UserInputID: "input-src", Status: userinput.StatusPending, CanRespond: true},
			},
		},
	}}

	mergeToolApprovals(turns, nil, nil)
	mergeUserInputs(turns, nil, nil)
	approval, input := turns[0].Messages[0].Approval, turns[0].Messages[1].UserInput
	if approval.CanApprove || input.CanRespond {
		t.Fatalf("snapshots without session rows stayed actionable: %#v %#v", approval, input)
	}
	if got := readOnlyApprovalIDs(turns); len(got) != 1 || got[0] != "approval-src" {
		t.Fatalf("readOnlyApprovalIDs = %v", got)
	}
	if got := readOnlyUserInputIDs(turns); len(got) != 1 || got[0] != "input-src" {
		t.Fatalf("readOnlyUserInputIDs = %v", got)
	}

	applyReadOnlyApprovals(turns, []toolapproval.Request{{ID: "approval-src", Status: toolapproval.StatusApproved, SelectedOptionID: "allow"}})
	applyReadOnlyUserInputs(turns, []userinput.Request{{ID: "input-src", Status: userinput.StatusSubmitted}})
	if approval.Status != toolapproval.StatusApproved || approval.SelectedOptionID != "allow" || approval.CanApprove {
		t.Fatalf("approval = %#v, want read-only approved", approval)
	}
	if input.Status != userinput.StatusSubmitted || input.CanRespond {
		t.Fatalf("user input = %#v, want read-only submitted", input)
	}
}
