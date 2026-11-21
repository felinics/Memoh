package view

import (
	"strings"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
)

// ApplyToolApprovals sets each tool block's approval card from its stored
// request. canApproveFn decides whether a pending card is still answerable;
// nil answers by status alone.
func ApplyToolApprovals(turns []UITurn, approvals []toolapproval.Request, canApproveFn func(toolapproval.Request) bool) {
	if len(turns) == 0 || len(approvals) == 0 {
		return
	}
	if canApproveFn == nil {
		canApproveFn = func(req toolapproval.Request) bool {
			return toolapproval.CanApprove(req.Status)
		}
	}
	byCallID := make(map[string]toolapproval.Request, len(approvals))
	for _, approval := range approvals {
		if callID := strings.TrimSpace(approval.ToolCallID); callID != "" {
			byCallID[callID] = approval
		}
	}
	for turnIdx := range turns {
		if turns[turnIdx].Role != "assistant" {
			continue
		}
		for msgIdx := range turns[turnIdx].Messages {
			msg := &turns[turnIdx].Messages[msgIdx]
			if msg.Type != UIMessageTool {
				continue
			}
			approval, ok := byCallID[strings.TrimSpace(msg.ToolCallID)]
			if !ok {
				continue
			}
			running := false
			msg.Running = &running
			msg.Approval = &UIToolApproval{
				ApprovalID:       approval.ID,
				ShortID:          approval.ShortID,
				Status:           approval.Status,
				DecisionReason:   approval.DecisionReason,
				CanApprove:       canApproveFn(approval),
				Options:          toolApprovalOptions(approval.Options),
				SelectedOptionID: approval.SelectedOptionID,
			}
		}
	}
}

func toolApprovalOptions(options []toolapproval.PermissionOption) []UIToolApprovalOption {
	if len(options) == 0 {
		return nil
	}
	converted := make([]UIToolApprovalOption, 0, len(options))
	for _, option := range options {
		converted = append(converted, UIToolApprovalOption{
			ID:   option.ID,
			Name: option.Name,
			Kind: option.Kind,
		})
	}
	return converted
}

// ApplyUserInputs sets each tool block's ask_user card from its stored request.
// canRespondFn decides whether a pending card is still answerable; nil lets a
// pending native request respond.
func ApplyUserInputs(turns []UITurn, requests []userinput.Request, canRespondFn func(userinput.Request) bool) {
	if len(turns) == 0 || len(requests) == 0 {
		return
	}
	byCallID := make(map[string]userinput.Request, len(requests))
	for _, req := range requests {
		if callID := strings.TrimSpace(req.ToolCallID); callID != "" {
			byCallID[callID] = req
		}
	}
	for turnIdx := range turns {
		if turns[turnIdx].Role != "assistant" {
			continue
		}
		for msgIdx := range turns[turnIdx].Messages {
			msg := &turns[turnIdx].Messages[msgIdx]
			if msg.Type != UIMessageTool {
				continue
			}
			req, ok := byCallID[strings.TrimSpace(msg.ToolCallID)]
			if !ok {
				continue
			}
			running := false
			msg.Running = &running
			canRespond := req.Status == userinput.StatusPending
			if canRespondFn != nil {
				canRespond = canRespondFn(req)
			}
			msg.UserInput = &UIUserInput{
				UserInputID: req.ID,
				ShortID:     req.ShortID,
				Status:      req.Status,
				Questions:   req.UIPayload.Questions,
				Answers:     userinput.AnswersFromResult(req.Result),
				CanRespond:  canRespond,
			}
		}
	}
}
