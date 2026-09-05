package application

import (
	"context"
	"errors"
	"fmt"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/runtimefence"
)

// finalizeRuntimeDecisions reuses decision cancellation without its model
// continuation. A parked native run has no waiter to cancel these rows when
// its execution context ends. Resolve only this run, under its original fence,
// before releasing ownership; never clear a successor's session-wide inputs.
func (s *Service) finalizeRuntimeDecisions(ctx context.Context, handle sessionruntime.RunHandle) error {
	if s.queries == nil {
		return nil
	}
	targets, err := s.PendingRuntimeDecisions(ctx, handle.RunID)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	if handle.FencingToken <= 0 {
		return sessionruntime.ErrRunOwnershipLost
	}
	ctx = runtimefence.WithContext(ctx, runtimefence.Fence{
		BotID: handle.BotID, SessionID: handle.SessionID, Token: handle.FencingToken,
	})
	for _, target := range targets {
		if target.BotID != handle.BotID || target.SessionID != handle.SessionID || target.FencingToken != handle.FencingToken {
			return sessionruntime.ErrRunOwnershipLost
		}
		var event native.StreamEvent
		switch target.Type {
		case sessionruntime.CommandUserInputResponse:
			if s.userInput == nil {
				return errors.New("user input service not configured")
			}
			req, err := s.userInput.Cancel(ctx, userinput.CancelInput{RequestID: target.ID, Reason: "run_ended"})
			if errors.Is(err, userinput.ErrAlreadyDecided) {
				continue
			}
			if err != nil {
				return fmt.Errorf("cancel run user input: %w", err)
			}
			event = native.StreamEvent{Type: native.EventUserInputRequest, UserInputID: req.ID,
				ToolName: req.ToolName, ToolCallID: req.ToolCallID, Status: req.Status,
				Input: req.Input, Metadata: userinput.DeferredMetadata(req)}
		case sessionruntime.CommandToolApprovalResponse:
			if s.toolApproval == nil {
				return errors.New("tool approval service not configured")
			}
			req, err := s.toolApproval.Reject(ctx, target.ID, "", "run_ended")
			if errors.Is(err, toolapproval.ErrAlreadyDecided) {
				continue
			}
			if err != nil {
				return fmt.Errorf("reject run tool approval: %w", err)
			}
			event = native.StreamEvent{Type: native.EventToolApprovalRequest, ApprovalID: req.ID,
				ToolName: req.ToolName, ToolCallID: req.ToolCallID, Status: req.Status,
				Input: req.ToolInput, Metadata: approvalResultMetadata(req)}
		}
		s.publishCommittedRuntimeDecision(ctx, sessionruntime.Command{
			BotID: handle.BotID, SessionID: handle.SessionID, RunID: handle.RunID,
			Generation: handle.Generation, TargetID: target.ID, Type: target.Type,
		}, event)
	}
	return nil
}
