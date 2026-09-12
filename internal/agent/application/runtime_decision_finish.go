package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/runtimefence"
)

// finalizeRuntimeDecisions reconciles durable decision state before releasing
// the run. Include already-decided rows: an inline runtime may have closed its
// event sink before its cancelled waiter published the terminal decision.
// Resolve only this run under its fence, without starting model continuation.
//
// Ownership/fencing comes from #865 (207844099); finish retry and reaper handoff
// come from #1107 (a23d24a1f). This callback adds decision cleanup to owner-side
// finalization; it does not replace that protocol or run on direct reaper finalization.
func (s *Service) finalizeRuntimeDecisions(ctx context.Context, handle sessionruntime.RunHandle) error {
	if s.queries == nil {
		return nil
	}
	targets, err := s.runtimeDecisionsForFinish(ctx, handle.RunID)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	if handle.FencingToken <= 0 {
		return sessionruntime.ErrRunOwnershipLost
	}
	for _, target := range targets {
		// A completed decision can belong to an earlier owner of this same
		// run. Reconcile it without rewriting it; only pending rows need the
		// current fence. Never touch a successor owner's decision.
		if target.BotID != handle.BotID || target.SessionID != handle.SessionID || target.RunID != handle.RunID ||
			target.FencingToken > handle.FencingToken ||
			(strings.EqualFold(target.Status, "pending") && target.FencingToken != handle.FencingToken) {
			return sessionruntime.ErrRunOwnershipLost
		}
	}
	ctx = runtimefence.WithContext(ctx, runtimefence.Fence{
		BotID: handle.BotID, SessionID: handle.SessionID, Token: handle.FencingToken,
	})
	for _, target := range targets {
		var event native.StreamEvent
		switch target.Type {
		case sessionruntime.CommandUserInputResponse:
			if s.userInput == nil {
				return errors.New("user input service not configured")
			}
			var req userinput.Request
			var err error
			if strings.EqualFold(target.Status, userinput.StatusPending) {
				req, err = s.userInput.Cancel(ctx, userinput.CancelInput{RequestID: target.ID, Reason: "run_ended"})
			} else {
				req, err = s.userInput.Get(ctx, target.ID)
			}
			if errors.Is(err, userinput.ErrAlreadyDecided) {
				req, err = s.userInput.Get(ctx, target.ID)
			}
			if err != nil {
				return fmt.Errorf("finalize run user input: %w", err)
			}
			if strings.EqualFold(req.Status, userinput.StatusPending) {
				return errors.New("run user input remained pending during finalization")
			}
			event = native.StreamEvent{
				Type: native.EventUserInputRequest, UserInputID: req.ID,
				ToolName: req.ToolName, ToolCallID: req.ToolCallID, Status: req.Status,
				Input: req.Input, Metadata: userinput.DeferredMetadata(req),
			}
		case sessionruntime.CommandToolApprovalResponse:
			if s.toolApproval == nil {
				return errors.New("tool approval service not configured")
			}
			var req toolapproval.Request
			var err error
			if strings.EqualFold(target.Status, toolapproval.StatusPending) {
				req, err = s.toolApproval.Reject(ctx, target.ID, "", "run_ended")
			} else {
				req, err = s.toolApproval.Get(ctx, target.ID)
			}
			if errors.Is(err, toolapproval.ErrAlreadyDecided) {
				req, err = s.toolApproval.Get(ctx, target.ID)
			}
			if err != nil {
				return fmt.Errorf("finalize run tool approval: %w", err)
			}
			if strings.EqualFold(req.Status, toolapproval.StatusPending) {
				return errors.New("run tool approval remained pending during finalization")
			}
			event = native.StreamEvent{
				Type: native.EventToolApprovalRequest, ApprovalID: req.ID,
				ToolName: req.ToolName, ToolCallID: req.ToolCallID, Status: req.Status,
				Input: req.ToolInput, Metadata: approvalResultMetadata(req),
			}
		}
		if s.decisionRuntime != nil {
			if _, err := s.decisionRuntime.HandleAgentEvent(ctx, handle, event); err != nil {
				// Keep ownership and use the existing finish retry. A successful
				// database decision is not proof that its projection was delivered.
				return fmt.Errorf("publish final runtime decision: %w", err)
			}
		}
	}
	return nil
}

func (s *Service) runtimeDecisionsForFinish(ctx context.Context, runID string) ([]sessionruntime.DecisionTarget, error) {
	id, err := db.ParseUUID(runID)
	if err != nil {
		return nil, err
	}
	approvals, err := s.queries.ListToolApprovalsByRun(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("read run tool approvals for finalization: %w", err)
	}
	inputs, err := s.queries.ListUserInputsByRun(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("read run user inputs for finalization: %w", err)
	}
	targets := make([]sessionruntime.DecisionTarget, 0, len(approvals)+len(inputs))
	for _, row := range approvals {
		targets = append(targets, toolApprovalDecisionTarget(row))
	}
	for _, row := range inputs {
		targets = append(targets, userInputDecisionTarget(row))
	}
	return targets, nil
}
