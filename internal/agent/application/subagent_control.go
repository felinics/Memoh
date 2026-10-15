package application

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/runtimekind"
)

func (s *Service) ownedSubagent(ctx context.Context, parent tools.SessionContext, childID string) (thread.Thread, error) {
	if s.sessionService == nil || parent.IsSubagent || parent.SessionType == thread.TypeSubagent {
		return thread.Thread{}, apperror.New(apperror.CodeRuntimeControlForbidden, nil)
	}
	parentThread, err := s.sessionService.Get(ctx, parent.SessionID)
	if err != nil {
		return thread.Thread{}, err
	}
	child, err := s.sessionService.Get(ctx, childID)
	if err != nil {
		return thread.Thread{}, err
	}
	if parentThread.BotID != parent.BotID || parentThread.SessionMode == thread.TypeSubagent || parentThread.Type == thread.TypeSubagent || child.BotID != parent.BotID || child.ParentThreadID != parent.SessionID || child.SessionMode != thread.TypeSubagent {
		return thread.Thread{}, apperror.New(apperror.CodeRuntimeControlForbidden, nil)
	}
	permission := bots.PermissionChat
	if runtimekind.IsExternal(child.RuntimeType) {
		permission = bots.PermissionWorkspaceExec
	}
	if s.botPermissions == nil {
		return thread.Thread{}, apperror.New(apperror.CodeRuntimeControlForbidden, nil)
	}
	allowed, err := s.botPermissions.HasBotPermission(ctx, parent.BotID, parent.UserID, permission)
	if err != nil {
		return thread.Thread{}, err
	}
	if !allowed {
		return thread.Thread{}, apperror.New(apperror.CodeRuntimeControlForbidden, nil)
	}
	return child, nil
}

func (s *Service) validateSubagentExecution(ctx context.Context, child thread.Thread) error {
	if !runtimekind.IsExternal(child.RuntimeType) {
		return nil
	}
	if metadataString(child.RuntimeMetadata, "external_termination_unconfirmed") != "" {
		return apperror.New(apperror.CodeExternalRuntimeTerminationUnconfirmed, nil)
	}
	agent, err := s.externalSubagentBackend(ctx, child.BotID, child.BotAgentID)
	if err != nil {
		return err
	}
	if botagents.SessionRuntime(agent.Runtime) != child.RuntimeType {
		return apperror.New(apperror.CodeBotAgentUnavailable, nil)
	}
	owner := metadataString(child.RuntimeMetadata, "runtime_owner_account_id")
	if owner == "" {
		owner = metadataString(child.Metadata, "runtime_owner_account_id")
	}
	return s.requireRuntimeOwnerWorkspaceExec(ctx, child.BotID, owner)
}

func subagentRuntimeState(snapshot sessionruntime.Snapshot) tools.SubagentRuntimeState {
	run := snapshot.CurrentRunView
	if run == nil {
		return tools.SubagentRuntimeState{Status: "idle"}
	}
	state := tools.SubagentRuntimeState{RunID: run.RunID, Status: run.Status, Code: run.ErrorCode, SteerSupported: sessionruntime.SteerRunAvailable(run)}
	switch run.Status {
	case sessionruntime.RunStatusAdmitting, sessionruntime.RunStatusRunning, sessionruntime.RunStatusWaitingDecision, sessionruntime.RunStatusAborting, sessionruntime.RunStatusFinishing:
		state.Active = true
	}
	return state
}

func unconfirmedSubagentRun(child thread.Thread, state tools.SubagentRuntimeState) string {
	if !runtimekind.IsExternal(child.RuntimeType) {
		return ""
	}
	if runID := metadataString(child.RuntimeMetadata, "external_termination_unconfirmed"); runID != "" {
		return runID
	}
	if runID := metadataString(child.RuntimeMetadata, "external_execution_run"); runID != "" && (!state.Active || state.RunID != runID) {
		return runID
	}
	return ""
}

func (s *Service) subagentStateSnapshot(ctx context.Context, child thread.Thread) (thread.Thread, tools.SubagentRuntimeState, error) {
	if s.sessionManager == nil {
		return child, tools.SubagentRuntimeState{}, sessionruntime.ErrManagerClosed
	}
	for range 3 {
		snapshot, err := s.sessionManager.Snapshot(ctx, child.BotID, child.ID)
		if err != nil {
			return child, tools.SubagentRuntimeState{}, err
		}
		state := subagentRuntimeState(snapshot)
		unconfirmed := unconfirmedSubagentRun(child, state)
		if unconfirmed == "" {
			if !state.Active && state.Code == string(apperror.CodeExternalRuntimeTerminationUnconfirmed) {
				state.Code = ""
			}
			return child, state, nil
		}
		unknown := tools.SubagentRuntimeState{RunID: unconfirmed, Status: "execution_unknown", Code: string(apperror.CodeExternalRuntimeTerminationUnconfirmed), Active: true}
		if state.Active && state.RunID == unconfirmed {
			return child, unknown, nil
		}
		next, settled := s.confirmSubagentTermination(ctx, child, unconfirmed)
		if !settled {
			latest, err := s.sessionService.Get(ctx, child.ID)
			if err != nil || executionMarkers(latest) == executionMarkers(child) {
				return child, unknown, nil
			}
			next = latest
		}
		child = next
	}
	return child, tools.SubagentRuntimeState{}, apperror.New(apperror.CodeSessionBusy, nil)
}

func executionMarkers(child thread.Thread) [2]string {
	return [2]string{metadataString(child.RuntimeMetadata, "external_execution_run"), metadataString(child.RuntimeMetadata, "external_termination_unconfirmed")}
}

// confirmSubagentTermination settles an unconfirmed run once a reap finds
// nothing of the child session left in the workspace: the child keeps its
// transcript, gives back its slot and may run again. A failed attempt keeps
// its claim until it expires.
func (s *Service) confirmSubagentTermination(ctx context.Context, child thread.Thread, runID string) (thread.Thread, bool) {
	ctx = context.WithoutCancel(ctx)
	claim, claimed, reaped := s.reapSubagent(ctx, child, runID)
	if !claimed || !reaped {
		return child, false
	}
	store := s.sessionService.(externalExecutionStore)
	attrs := []slog.Attr{slog.String("session_id", child.ID), slog.String("run_id", runID)}
	resolveCtx, cancelResolve := errs.WithTimeout(ctx, terminalWriteTimeout)
	defer cancelResolve()
	resolved, ok, err := store.ResolveExternalSubagentTermination(resolveCtx, child.ID, runID, claim)
	if err != nil || !ok {
		if err == nil {
			err = errs.New("subagent termination claim lost")
		}
		s.logSubagentReap(ctx, errs.Wrap(err, "record subagent termination", attrs...))
		return child, false
	}
	s.logger.LogAttrs(ctx, slog.LevelInfo, "external subagent termination confirmed", attrs...)
	return resolved, true
}

// reapSubagent stops what the child session left in the workspace on behalf
// of runID. The claim keeps a second attempt from overlapping it and from
// stopping a later run once the session no longer records runID.
func (s *Service) reapSubagent(ctx context.Context, child thread.Thread, runID string) (claim string, claimed, reaped bool) {
	store, ok := s.sessionService.(externalExecutionStore)
	if s.subagentReaper == nil || !ok {
		return "", false, false
	}
	ctx = context.WithoutCancel(ctx)
	attrs := []slog.Attr{slog.String("session_id", child.ID), slog.String("run_id", runID)}
	claimCtx, cancelClaim := errs.WithTimeout(ctx, terminalWriteTimeout)
	claim, claimed, err := store.ClaimExternalSubagentReap(claimCtx, child.ID, runID, subagentReapLease)
	cancelClaim()
	if err != nil {
		s.logSubagentReap(ctx, errs.Wrap(err, "claim subagent termination", attrs...))
	}
	if !claimed {
		return "", false, false
	}
	reapCtx, cancel := context.WithTimeout(ctx, subagentReapTimeout)
	defer cancel()
	if err := s.subagentReaper.Reap(reapCtx, child.BotID, child.ID); err != nil {
		s.logSubagentReap(ctx, errs.Wrap(err, "stop subagent run leftovers", attrs...))
		return claim, true, false
	}
	return claim, true, true
}

func (s *Service) logSubagentReap(ctx context.Context, err error) {
	result := errlog.Event(ctx, "agent.subagent_reap", err, errlog.Options{})
	s.logger.LogAttrs(ctx, result.Level, "external subagent termination still unconfirmed", result.Attrs()...)
}

func (s *Service) SnapshotSubagent(ctx context.Context, parent tools.SessionContext, childID string) (tools.SubagentRuntimeState, error) {
	state, err := s.snapshotSubagent(ctx, parent, childID)
	return state, s.subagentToolError(ctx, "agent.subagent_state", err)
}

func (s *Service) snapshotSubagent(ctx context.Context, parent tools.SessionContext, childID string) (tools.SubagentRuntimeState, error) {
	child, err := s.ownedSubagent(ctx, parent, childID)
	if err != nil {
		return tools.SubagentRuntimeState{}, err
	}
	child, state, err := s.subagentStateSnapshot(ctx, child)
	if err != nil {
		return tools.SubagentRuntimeState{}, err
	}
	if state.Status == "execution_unknown" {
		return state, nil
	}
	if err := s.validateSubagentExecution(ctx, child); err != nil {
		state.Code = string(apperror.CodeOf(s.subagentToolError(ctx, "agent.subagent_state", err)))
		state.SteerSupported = false
	}
	return state, nil
}

func (s *Service) WaitSubagentIdle(ctx context.Context, parent tools.SessionContext, childID string) error {
	if s.sessionManager == nil {
		return sessionruntime.ErrManagerClosed
	}
	if _, err := s.ownedSubagent(ctx, parent, childID); err != nil {
		return err
	}
	subscription, err := s.sessionManager.Subscribe(ctx, parent.BotID, childID)
	if err != nil {
		return err
	}
	defer subscription.Close()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		child, err := s.ownedSubagent(ctx, parent, childID)
		if err != nil {
			return err
		}
		child, state, err := s.subagentStateSnapshot(ctx, child)
		if err != nil {
			return err
		}
		if state.Status == "execution_unknown" {
			return apperror.New(apperror.CodeExternalRuntimeTerminationUnconfirmed, nil)
		}
		if err := s.validateSubagentExecution(ctx, child); err != nil {
			return err
		}
		if !state.Active {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-subscription.C:
			if !ok {
				return sessionruntime.ErrManagerClosed
			}
		case <-ticker.C:
		}
	}
}

func (s *Service) ControlSubagent(ctx context.Context, input tools.SubagentControlInput) (tools.SubagentControlResult, error) {
	result, err := s.controlSubagent(ctx, input)
	failure := QueueAdmissionFailureOf(err)
	if errors.Is(err, sessionruntime.ErrCommandTargetMismatch) || (failure != QueueAdmissionNotRefused && failure != QueueAdmissionUnavailable) {
		return result, err
	}
	if failure == QueueAdmissionUnavailable {
		err = apperror.Wrap(apperror.CodeQueueAdmissionUnavailable, err, nil)
	}
	return result, s.subagentToolError(ctx, "agent.subagent_control", err)
}

func (s *Service) controlSubagent(ctx context.Context, input tools.SubagentControlInput) (tools.SubagentControlResult, error) {
	child, err := s.ownedSubagent(ctx, input.Parent, input.SessionID)
	if err != nil {
		return tools.SubagentControlResult{}, err
	}
	if s.sessionManager == nil {
		return tools.SubagentControlResult{}, sessionruntime.ErrManagerClosed
	}
	if strings.TrimSpace(input.RunID) == "" {
		return tools.SubagentControlResult{}, sessionruntime.ErrQueueInvalidReference
	}
	child, state, err := s.subagentStateSnapshot(ctx, child)
	if err != nil {
		return tools.SubagentControlResult{}, err
	}
	result := tools.SubagentControlResult{SubagentRuntimeState: state}
	if state.Status == "execution_unknown" {
		if input.RunID != state.RunID {
			return result, sessionruntime.ErrCommandTargetMismatch
		}
		if input.Action == "stop" {
			return result, nil
		}
		return result, apperror.New(apperror.CodeExternalRuntimeTerminationUnconfirmed, nil)
	}
	if state.RunID != input.RunID {
		return result, sessionruntime.ErrCommandTargetMismatch
	}
	if input.ControlID == "" {
		input.ControlID = uuid.NewString()
	}
	switch input.Action {
	case "stop":
		result.Accepted, err = s.AbortRuntimeRun(ctx, child.BotID, child.ID, input.RunID, input.ControlID)
		if err != nil {
			return result, err
		}
		if result.Accepted {
			result.Status = "stopping"
			latest, readErr := s.sessionManager.Snapshot(ctx, child.BotID, child.ID)
			if readErr == nil && latest.CurrentRunView != nil && latest.CurrentRunView.RunID == input.RunID {
				result.SubagentRuntimeState = subagentRuntimeState(latest)
			}
		}
		return result, nil
	case "steer":
		if !state.SteerSupported {
			return result, sessionruntime.ErrQueueSteerUnsupported
		}
		if err := s.validateSubagentExecution(ctx, child); err != nil {
			return result, err
		}
		cmd, err := queueTurnCommand(QueueInput{TeamID: child.TeamID, BotID: child.BotID, SessionID: child.ID, InvocationID: input.ControlID, UserID: input.Parent.UserID, SourceChannelIdentityID: input.Parent.ChannelIdentityID, Text: input.Message})
		if err != nil {
			return result, err
		}
		payload, err := encodeQueueCommand(cmd)
		if err != nil {
			return result, err
		}
		item, err := s.sessionManager.EnqueueSteerForRun(ctx, sessionruntime.Key{BotID: child.BotID, SessionID: child.ID}, input.RunID, uuid.NewString(), input.ControlID, payload)
		if err != nil {
			return result, err
		}
		result.Accepted, result.ItemID = item.Status == sessionruntime.QueueAccepted, string(item.ID)
		return result, nil
	default:
		return result, apperror.New(apperror.CodeRuntimeControlRequestInvalid, nil)
	}
}
