package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/decision"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type continuationLifecycleResult struct {
	snapshot *contextfrag.LifecycleSnapshot
	cause    error
	deferred bool
}

// ResolveRuntimeDecision reads the authoritative decision row before any live
// owner lookup. Terminal rows are returned too: the router needs to distinguish
// a known, already-decided request from an unfenced ACP/MCP request.
func (s *Service) ResolveRuntimeDecision(ctx context.Context, commandType, decisionID string) (sessionruntime.DecisionTarget, error) {
	if s == nil || s.queries == nil {
		return sessionruntime.DecisionTarget{}, errors.New("runtime decision store is not configured")
	}
	id, err := db.ParseUUID(decisionID)
	if err != nil {
		return sessionruntime.DecisionTarget{}, fmt.Errorf("%w: %w", sessionruntime.ErrDecisionNotFound, err)
	}
	switch commandType {
	case sessionruntime.CommandToolApprovalResponse:
		row, err := s.queries.GetToolApprovalRequest(ctx, id)
		if err != nil {
			return sessionruntime.DecisionTarget{}, runtimeDecisionReadError(err)
		}
		return toolApprovalDecisionTarget(row), nil
	case sessionruntime.CommandUserInputResponse:
		row, err := s.queries.GetUserInputRequest(ctx, id)
		if err != nil {
			return sessionruntime.DecisionTarget{}, runtimeDecisionReadError(err)
		}
		return userInputDecisionTarget(row), nil
	default:
		return sessionruntime.DecisionTarget{}, fmt.Errorf("unsupported runtime decision command %q", commandType)
	}
}

// PendingRuntimeDecisions resolves every durable decision that parked runID.
// It is used only by expired-owner recovery, where preserving the exact rows
// is required before advancing the run's fencing token — a turn can park on
// several approvals and user inputs at once, and dropping any of them here
// would supersede a decision the user can still answer.
func (s *Service) PendingRuntimeDecisions(ctx context.Context, runID string) ([]sessionruntime.DecisionTarget, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("runtime decision store is not configured")
	}
	id, err := db.ParseUUID(runID)
	if err != nil {
		return nil, err
	}
	approvals, err := s.queries.ListPendingToolApprovalsByRun(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("read pending tool approvals for run: %w", err)
	}
	inputs, err := s.queries.ListPendingUserInputsByRun(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("read pending user inputs for run: %w", err)
	}
	out := make([]sessionruntime.DecisionTarget, 0, len(approvals)+len(inputs))
	for _, approval := range approvals {
		out = append(out, toolApprovalDecisionTarget(approval))
	}
	for _, input := range inputs {
		out = append(out, userInputDecisionTarget(input))
	}
	// Recovery decides by session runtime whether the parked run is
	// resumable at all; every pending decision here shares one session.
	if len(out) > 0 && s.sessionService != nil {
		if sess, sessErr := s.sessionService.Get(ctx, out[0].SessionID); sessErr == nil {
			for i := range out {
				out[i].SessionRuntime = sess.RuntimeType
			}
		}
	}
	return out, nil
}

func runtimeDecisionReadError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return sessionruntime.ErrDecisionNotFound
	}
	return err
}

func toolApprovalDecisionTarget(row dbsqlc.ToolApprovalRequest) sessionruntime.DecisionTarget {
	return sessionruntime.DecisionTarget{
		Type: sessionruntime.CommandToolApprovalResponse, ID: decisionUUIDString(row.ID),
		BotID: decisionUUIDString(row.BotID), SessionID: decisionUUIDString(row.SessionID),
		RunID: decisionUUIDString(row.RunID), TurnID: decisionUUIDString(row.TurnID),
		Status: row.Status, FencingToken: pgInt64(row.RuntimeFencingToken),
		ControlID: pgText(row.ResponseControlID), PayloadHash: pgText(row.ResponsePayloadHash),
	}
}

func userInputDecisionTarget(row dbsqlc.UserInputRequest) sessionruntime.DecisionTarget {
	return sessionruntime.DecisionTarget{
		Type: sessionruntime.CommandUserInputResponse, ID: decisionUUIDString(row.ID),
		BotID: decisionUUIDString(row.BotID), SessionID: decisionUUIDString(row.SessionID),
		RunID: decisionUUIDString(row.RunID), TurnID: decisionUUIDString(row.TurnID),
		Status: row.Status, FencingToken: pgInt64(row.RuntimeFencingToken),
		ControlID: pgText(row.ResponseControlID), PayloadHash: pgText(row.ResponsePayloadHash),
	}
}

func decisionUUIDString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func pgInt64(value pgtype.Int8) int64 {
	if !value.Valid {
		return 0
	}
	return value.Int64
}

func pgText(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func (s *Service) routeToolApprovalResponse(ctx context.Context, input ToolApprovalResponseInput) (bool, error) {
	if s == nil || s.decisionRuntime == nil {
		return false, nil
	}
	if input.ControlID == "" {
		input.ControlID = implicitDecisionControlID(sessionruntime.CommandToolApprovalResponse, firstNonEmpty(input.ExplicitID, input.ApprovalID))
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return true, err
	}
	result, err := s.decisionRuntime.RouteDecisionResponse(ctx, sessionruntime.DecisionResponse{
		ControlID: input.ControlID, Type: sessionruntime.CommandToolApprovalResponse,
		DecisionID: firstNonEmpty(input.ExplicitID, input.ApprovalID),
		BotID:      input.BotID, SessionID: input.ThreadID, Payload: payload,
	})
	if errors.Is(err, sessionruntime.ErrDecisionNotFound) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if !result.Handled {
		return false, nil
	}
	if !result.Applied {
		return true, toolapproval.ErrAlreadyDecided
	}
	return true, nil
}

func (s *Service) routeUserInputResponse(ctx context.Context, input UserInputResponseInput) (bool, error) {
	if s == nil || s.decisionRuntime == nil {
		return false, nil
	}
	if input.ControlID == "" {
		input.ControlID = implicitDecisionControlID(sessionruntime.CommandUserInputResponse, firstNonEmpty(input.ExplicitID, input.UserInputID))
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return true, err
	}
	result, err := s.decisionRuntime.RouteDecisionResponse(ctx, sessionruntime.DecisionResponse{
		ControlID: input.ControlID, Type: sessionruntime.CommandUserInputResponse,
		DecisionID: firstNonEmpty(input.ExplicitID, input.UserInputID),
		BotID:      input.BotID, SessionID: input.ThreadID, Payload: payload,
	})
	if errors.Is(err, sessionruntime.ErrDecisionNotFound) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if !result.Handled {
		return false, nil
	}
	if !result.Applied {
		return true, userinput.ErrAlreadyDecided
	}
	return true, nil
}

func implicitDecisionControlID(commandType, decisionID string) string {
	return "implicit:" + commandType + ":" + decisionID
}

// handleRuntimeDecisionCommand commits on the routed-command deadline, then
// continues independently on the owning run. The command result therefore
// means "the decision was durably accepted", not "the model finished".
//
//nolint:contextcheck // the continuation is rooted in the owning run, not the acknowledgement request.
func (s *Service) handleRuntimeDecisionCommand(ctx context.Context, command sessionruntime.Command) error {
	if s == nil || s.decisionRuntime == nil {
		return errors.New("runtime decision handler is not configured")
	}
	runCtx, runCancel, err := s.decisionRuntime.DecisionContinuationContext(command)
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
		ctx = decision.WithResponseIdentity(ctx, decision.ResponseIdentity{
			ControlID: input.ControlID, PayloadHash: command.PayloadHash,
		})

		committed, err := s.CommitUserInputResponse(ctx, input)
		if err != nil {
			runCancel()
			return err
		}
		committed.runID = command.RunID
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
			s.continueRuntimeDecision(runCtx, command, func(
				continuationCtx context.Context,
				lifecycle *continuationLifecycleResult,
				eventCh chan<- WSStreamEvent,
			) error {
				return s.continueCommittedUserInputResponse(continuationCtx, committed, lifecycle, eventCh)
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
		ctx = decision.WithResponseIdentity(ctx, decision.ResponseIdentity{
			ControlID: input.ControlID, PayloadHash: command.PayloadHash,
		})

		committed, err := s.CommitToolApprovalResponse(ctx, input)
		if err != nil {
			runCancel()
			return err
		}
		committed.runID = command.RunID
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
			s.continueRuntimeDecision(runCtx, command, func(
				continuationCtx context.Context,
				lifecycle *continuationLifecycleResult,
				eventCh chan<- WSStreamEvent,
			) error {
				return s.continueCommittedToolApprovalResponse(continuationCtx, committed, lifecycle, eventCh)
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

func (s *Service) continueRuntimeDecision(
	ctx context.Context,
	command sessionruntime.Command,
	continueRun func(context.Context, *continuationLifecycleResult, chan<- WSStreamEvent) error,
) {
	handle := sessionruntime.RunHandle{
		BotID:      command.BotID,
		SessionID:  command.SessionID,
		RunID:      command.RunID,
		Generation: command.Generation,
	}
	if err := s.decisionRuntime.WaitDecisionContinuationReady(ctx, command); err != nil {
		s.recoverContextLifecycleFromAssistantMetadata(ctx, command.RunID, command.BotID, command.SessionID, err)
		s.finishRuntimeDecision(ctx, handle, err)
		return
	}
	eventCh := make(chan WSStreamEvent, 64)
	runDone := make(chan error, 1)
	lifecycle := &continuationLifecycleResult{}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		runDone <- continueRun(runCtx, lifecycle, eventCh)
		close(eventCh)
	}()

	var (
		publishErr        error
		eventCause        error
		lifecycleDeferred bool
	)
	for raw := range eventCh {
		var event native.StreamEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			continue
		}
		if eventErr := agentStreamLifecycleError(event); eventErr != nil && eventCause == nil {
			eventCause = eventErr
		}
		if event.IsTerminal() {
			lifecycleDeferred = pendingContinuationDecision(event)
			if !lifecycleDeferred {
				switch event.Type {
				case native.EventAgentEnd:
					eventCause = nil
				case native.EventAgentAbort:
					if context.Cause(ctx) != nil || eventCause == nil {
						eventCause = agentAbortCause(ctx)
					}
				}
			}
		}
		if _, err := s.decisionRuntime.HandleAgentEvent(runCtx, handle, event); err != nil {
			publishErr = err
			cancel()
			break
		}
	}
	runErr := <-runDone
	lifecycleDeferred = lifecycleDeferred || lifecycle.deferred
	lifecycleCause := firstLifecycleCause(runErr, eventCause, lifecycle.cause)
	if publishErr != nil {
		runErr = publishErr
		lifecycleCause = publishErr
		lifecycleDeferred = false
	}
	if runErr != nil {
		s.persistRuntimeDecisionLifecycle(ctx, command, lifecycle, lifecycleCause)
		s.finishRuntimeDecision(ctx, handle, runErr)
		return
	}
	if lifecycleDeferred {
		_ = s.decisionRuntime.FinishRun(context.WithoutCancel(ctx), handle, "", "")
		return
	}
	s.persistRuntimeDecisionLifecycle(ctx, command, lifecycle, lifecycleCause)
	s.finishRuntimeDecision(ctx, handle, lifecycleCause)
}

func firstLifecycleCause(causes ...error) error {
	for _, cause := range causes {
		if cause != nil {
			return cause
		}
	}
	return nil
}

func pendingContinuationDecision(event native.StreamEvent) bool {
	if !event.IsTerminal() ||
		(strings.TrimSpace(event.ApprovalID) == "" && strings.TrimSpace(event.UserInputID) == "") {
		return false
	}
	status := strings.TrimSpace(event.Status)
	return status == "" || strings.EqualFold(status, "pending")
}

func (s *Service) persistRuntimeDecisionLifecycle(
	ctx context.Context,
	command sessionruntime.Command,
	result *continuationLifecycleResult,
	cause error,
) {
	if result != nil && result.snapshot != nil {
		s.persistContextLifecycleSnapshot(
			ctx,
			command.RunID,
			command.BotID,
			command.SessionID,
			result.snapshot,
			cause,
			true,
		)
		return
	}
	s.recoverContextLifecycleFromAssistantMetadata(
		ctx,
		command.RunID,
		command.BotID,
		command.SessionID,
		cause,
	)
}

func (s *Service) finishRuntimeDecision(ctx context.Context, handle sessionruntime.RunHandle, cause error) {
	status, message := runtimeDecisionTerminal(ctx, cause)
	lifecycleCtx := frozenContextCause(ctx)
	minimal := minimalContextLifecycleSnapshot()
	staged := s.stageContextLifecycleCandidate(
		lifecycleCtx,
		handle.RunID,
		handle.BotID,
		handle.SessionID,
		&minimal,
		cause,
		contextLifecycleCandidateMinimal,
	)
	if err := s.decisionRuntime.FinishRun(context.WithoutCancel(nonNilContext(ctx)), handle, status, message); err == nil && !staged {
		s.EnsureTerminalContextLifecycle(
			lifecycleCtx,
			handle.RunID,
			handle.BotID,
			handle.SessionID,
			cause,
		)
	}
}

func frozenContextCause(ctx context.Context) context.Context {
	ctx = nonNilContext(ctx)
	frozen := context.WithoutCancel(ctx)
	cause := context.Cause(ctx)
	if cause == nil {
		return frozen
	}
	frozen, cancel := context.WithCancelCause(frozen)
	cancel(cause)
	return frozen
}

func runtimeDecisionTerminal(ctx context.Context, cause error) (string, string) {
	explicitlyCanceled := ctx != nil &&
		errors.Is(cause, context.Canceled) &&
		errors.Is(context.Cause(ctx), context.Canceled)
	if cause != nil && !explicitlyCanceled {
		return sessionruntime.RunStatusErrored, string(apperror.CodeOf(cause))
	}
	return "", ""
}
