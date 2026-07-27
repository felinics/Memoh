package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	userinput "github.com/memohai/memoh/internal/agent/decision/input"
	"github.com/memohai/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/memohai/memoh/internal/agent/runtime/session"
)

// handleRuntimeDecisionCommand commits on the routed-command deadline, then
// continues independently on the owning run. The command result therefore
// means "the decision was durably accepted", not "the model finished".
func (s *Service) handleRuntimeDecisionCommand(ctx context.Context, command sessionruntime.Command) error {
	if s == nil || s.decisionRuntime == nil {
		return errors.New("runtime decision handler is not configured")
	}
	runCtx, runCancel, err := s.decisionRuntime.DecisionContinuationContext(ctx, command)
	if err != nil {
		return err
	}
	switch command.Type {
	case sessionruntime.CommandUserInputResponse:
		var input UserInputResponseInput
		if err := json.Unmarshal(command.Payload, &input); err != nil {
			runCancel()
			return err
		}
		input.BotID = command.BotID
		input.ThreadID = command.SessionID
		input.UserInputID = command.TargetID
		input.ExplicitID = command.TargetID
		input.ReplyExternalMessageID = ""
		input.ChatToken = ""
		input.SuppressActivePromptAttach = true

		committed, err := s.CommitUserInputResponse(ctx, input)
		if err != nil {
			runCancel()
			return err
		}
		s.publishCommittedRuntimeDecision(runCtx, command, native.StreamEvent{
			Type:        native.EventUserInputRequest,
			ToolName:    committed.request.ToolName,
			ToolCallID:  committed.request.ToolCallID,
			UserInputID: committed.request.ID,
			ShortID:     committed.request.ShortID,
			Status:      committed.request.Status,
			Input:       committed.request.Input,
			Metadata:    userinput.DeferredMetadata(committed.request),
		})
		go func() {
			defer runCancel()
			s.continueRuntimeDecision(runCtx, command, func(eventCh chan<- WSStreamEvent) error {
				return s.ContinueCommittedUserInputResponse(runCtx, committed, eventCh)
			})
		}()
		return nil
	case sessionruntime.CommandToolApprovalResponse:
		var input ToolApprovalResponseInput
		if err := json.Unmarshal(command.Payload, &input); err != nil {
			runCancel()
			return err
		}
		input.BotID = command.BotID
		input.ThreadID = command.SessionID
		input.ApprovalID = command.TargetID
		input.ExplicitID = command.TargetID
		input.ReplyExternalMessageID = ""
		input.ChatToken = ""
		input.SuppressActivePromptAttach = true

		committed, err := s.CommitToolApprovalResponse(ctx, input)
		if err != nil {
			runCancel()
			return err
		}
		s.publishCommittedRuntimeDecision(runCtx, command, native.StreamEvent{
			Type:       native.EventToolApprovalRequest,
			ToolName:   committed.request.ToolName,
			ToolCallID: committed.request.ToolCallID,
			ApprovalID: committed.request.ID,
			ShortID:    committed.request.ShortID,
			Status:     committed.request.Status,
			Input:      committed.request.ToolInput,
			Metadata:   approvalResultMetadata(committed.request),
		})
		go func() {
			defer runCancel()
			s.continueRuntimeDecision(runCtx, command, func(eventCh chan<- WSStreamEvent) error {
				return s.ContinueCommittedToolApprovalResponse(runCtx, committed, eventCh)
			})
		}()
		return nil
	default:
		runCancel()
		return errors.New("unsupported runtime decision command")
	}
}

// publishCommittedRuntimeDecision replaces the pending decision in the live
// projection before the command is acknowledged. PostgreSQL remains the
// correctness boundary: once Commit*Response succeeds the user's answer is
// accepted even if publishing the derived live view fails, so publication
// errors are logged and the continuation still runs.
func (s *Service) publishCommittedRuntimeDecision(ctx context.Context, command sessionruntime.Command, event native.StreamEvent) {
	if s == nil || s.decisionRuntime == nil {
		return
	}
	handle := sessionruntime.RunHandle{
		BotID:      command.BotID,
		SessionID:  command.SessionID,
		RunID:      command.RunID,
		Generation: command.Generation,
	}
	if _, err := s.decisionRuntime.HandleAgentEvent(ctx, handle, event); err != nil && s.logger != nil {
		s.logger.Warn("publish committed runtime decision failed",
			slog.Any("error", err),
			slog.String("run_id", command.RunID),
			slog.String("decision_id", command.TargetID),
			slog.String("command_type", command.Type))
	}
}

func (s *Service) continueRuntimeDecision(ctx context.Context, command sessionruntime.Command, continueRun func(chan<- WSStreamEvent) error) {
	handle := sessionruntime.RunHandle{
		BotID:      command.BotID,
		SessionID:  command.SessionID,
		RunID:      command.RunID,
		Generation: command.Generation,
	}
	eventCh := make(chan WSStreamEvent, 64)
	runDone := make(chan error, 1)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		runDone <- continueRun(eventCh)
		close(eventCh)
	}()

	var publishErr error
	for raw := range eventCh {
		var event native.StreamEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			continue
		}
		if _, err := s.decisionRuntime.HandleAgentEvent(runCtx, handle, event); err != nil {
			publishErr = err
			cancel()
			break
		}
	}
	runErr := <-runDone
	if publishErr != nil {
		runErr = publishErr
	}
	finishCtx := context.WithoutCancel(ctx)
	if runErr != nil {
		_ = s.decisionRuntime.FinishRun(finishCtx, handle, sessionruntime.RunStatusErrored, runErr.Error())
		return
	}
	_ = s.decisionRuntime.FinishRun(finishCtx, handle, "", "")
}
