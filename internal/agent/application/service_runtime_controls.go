package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/workspace"
)

// RuntimeControlRequest is scoped to a thread and its current actor.
// Drivers may query their runtime process for current control state.
type RuntimeControlRequest = turn.RuntimeControlRequest

func (s *Service) runtimeControlSession(ctx context.Context, request RuntimeControlRequest) (session.Thread, error) {
	if request.TeamID != "" && s.allowedTeam != "" && request.TeamID != s.allowedTeam {
		return session.Thread{}, turn.ErrTeamNotServed
	}
	if s.sessionService == nil || s.botPermissions == nil {
		return session.Thread{}, external.ErrControlUnsupported
	}
	sess, err := s.sessionService.Get(ctx, request.ThreadID)
	if err != nil {
		return sess, err
	}
	if sess.BotID != request.BotID || strings.TrimSpace(request.ActorID) == "" {
		return sess, approval.ErrForbidden
	}
	return sess, nil
}

func (s *Service) runtimeControlTarget(ctx context.Context, request RuntimeControlRequest) (session.Thread, external.Driver, external.PromptInput, error) {
	sess, err := s.runtimeControlSession(ctx, request)
	if err != nil {
		return sess, nil, external.PromptInput{}, err
	}
	manage, err := s.botPermissions.HasBotPermission(ctx, request.BotID, request.ActorID, bots.PermissionManage)
	if err != nil {
		return sess, nil, external.PromptInput{}, err
	}
	if !manage {
		if sess.CreatedByUserID != request.ActorID {
			return sess, nil, external.PromptInput{}, approval.ErrForbidden
		}
		permission := bots.PermissionChat
		if session.UsesDecisionWaiter(sess) {
			permission = bots.PermissionWorkspaceExec
		}
		allowed, err := s.botPermissions.HasBotPermission(ctx, request.BotID, request.ActorID, permission)
		if err != nil {
			return sess, nil, external.PromptInput{}, err
		}
		if !allowed {
			return sess, nil, external.PromptInput{}, approval.ErrForbidden
		}
	}
	return sess, s.externalDrivers[sess.RuntimeType], runtimeControlInput(sess, request), nil
}

func runtimeControlInput(sess session.Thread, request RuntimeControlRequest) external.PromptInput {
	meta := runtimeSessionMeta(sess)
	input := external.PromptInput{
		BotID: sess.BotID, BotAgentID: sess.BotAgentID, ThreadID: sess.ID,
		Language: request.Language, RuntimeMetadata: meta, Command: request.Command,
		RuntimeOwnerAccountID: metadataString(meta, "runtime_owner_account_id"),
		ChannelIdentityID:     request.ActorID, ToolHTTPURL: request.ToolHTTPURL,
	}
	return input
}

func (s *Service) RuntimeControls(ctx context.Context, request RuntimeControlRequest) (out external.Controls, resultErr error) {
	defer func() { resultErr = publicRuntimeControlError(resultErr) }()
	_, driver, input, err := s.runtimeControlTarget(ctx, request)
	if err != nil {
		return external.Controls{}, err
	}
	return external.ReadControls(ctx, driver, input)
}

// RuntimeCommands reads declarations without fetching unrelated runtime state.
// Catalog discovery needs chat access, not ownership of the runtime controls.
// Executing a declared command still uses the existing control/admission gates.
func (s *Service) RuntimeCommands(ctx context.Context, request RuntimeControlRequest) (commands []external.Command, err error) {
	defer func() { err = publicRuntimeControlError(err) }()
	sess, err := s.runtimeControlSession(ctx, request)
	if err != nil {
		return nil, err
	}
	allowed, err := s.botPermissions.HasBotPermission(ctx, request.BotID, request.ActorID, bots.PermissionChat)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, approval.ErrForbidden
	}
	if provider, ok := s.externalDrivers[sess.RuntimeType].(external.CommandProvider); ok {
		return provider.Commands(ctx, runtimeControlInput(sess, request))
	}
	return []external.Command{}, nil
}

func (s *Service) RuntimeGoal(ctx context.Context, request RuntimeControlRequest) (goal *external.Goal, err error) {
	defer func() { err = publicRuntimeControlError(err) }()
	_, driver, input, err := s.runtimeControlTarget(ctx, request)
	if err != nil {
		return nil, err
	}
	provider, ok := driver.(external.GoalProvider)
	if !ok {
		return nil, external.ErrControlUnsupported
	}
	return provider.Goal(ctx, input)
}

func (s *Service) SetRuntimeMode(ctx context.Context, request RuntimeControlRequest) (out external.ModeState, err error) {
	defer func() { err = publicRuntimeControlError(err) }()
	_, driver, _, err := s.runtimeControlTarget(ctx, request)
	if err != nil {
		return out, err
	}
	if _, _, _, err := runtimeModeAccessors(driver, request.ModeKind); err != nil {
		return out, err
	}

	err = s.runRuntimeControl(ctx, request, func(ctx context.Context, sess session.Thread, driver external.Driver, input external.PromptInput) error {
		read, set, metadataKey, err := runtimeModeAccessors(driver, request.ModeKind)
		if err != nil {
			return err
		}
		modes, err := read(ctx, input)
		if err != nil {
			return err
		}
		valid := false
		for _, mode := range modes.AvailableModes {
			if mode.ID == request.ModeID {
				valid = true
				break
			}
		}
		// An unsupported snapshot may only mean the runtime is cold (ACP
		// reads its live session); let the driver's own set decide then.
		if modes.Supported && !valid {
			return external.ErrModeUnavailable
		}
		out, err = set(ctx, input, request.ModeID)
		if err != nil {
			return err
		}
		_, err = s.sessionService.MergeRuntimeMetadata(ctx, sess.ID, sess.RuntimeType, map[string]any{metadataKey: out.CurrentModeID})
		return err
	})
	return out, err
}

type (
	readRuntimeMode func(context.Context, external.PromptInput) (external.ModeState, error)
	setRuntimeMode  func(context.Context, external.PromptInput, string) (external.ModeState, error)
)

func runtimeModeAccessors(driver external.Driver, kind string) (readRuntimeMode, setRuntimeMode, string, error) {
	switch kind {
	case "", "permission":
		if provider, ok := driver.(external.ModeProvider); ok {
			return provider.Modes, provider.SetMode, "permission_mode", nil
		}
	case "plan":
		if provider, ok := driver.(external.PlanModeProvider); ok {
			return provider.PlanMode, provider.SetPlanMode, "collaboration_mode", nil
		}
	default:
		return nil, nil, "", apperror.New(apperror.CodeRuntimeControlRequestInvalid, nil)
	}
	return nil, nil, "", external.ErrControlUnsupported
}

// ExecuteRuntimeCommand handles commands which do not create a conversation
// turn. Turn commands go through the existing chat admission/persistence path.
func (s *Service) ExecuteRuntimeCommand(ctx context.Context, request RuntimeControlRequest) (text string, resultErr error) {
	defer func() { resultErr = publicRuntimeControlError(resultErr) }()
	_, driver, input, err := s.runtimeControlTarget(ctx, request)
	if err != nil {
		return "", err
	}
	provider, ok := driver.(external.CommandProvider)
	if !ok {
		return "", external.ErrCommandUnavailable
	}
	commands, err := provider.Commands(ctx, input)
	if err != nil {
		return "", err
	}
	command, ok := external.FindCommand(commands, request.Command)
	if !ok {
		return "", external.ErrCommandUnavailable
	}
	if command.Kind == external.CommandRead {
		controlCtx, err := s.runtimeControlContext(ctx, request)
		if err != nil {
			return "", err
		}
		return provider.ReadCommand(controlCtx, input)
	}
	if command.Kind != external.CommandOperation {
		return "", external.ErrCommandUnavailable
	}
	err = s.runRuntimeControl(ctx, request, func(ctx context.Context, sess session.Thread, driver external.Driver, input external.PromptInput) error {
		provider, ok := driver.(external.CommandProvider)
		if !ok {
			return external.ErrCommandUnavailable
		}
		commands, err := provider.Commands(ctx, input)
		if err != nil {
			return err
		}
		current, ok := external.FindCommand(commands, request.Command)
		if !ok || current.Kind != external.CommandOperation {
			return external.ErrCommandUnavailable
		}
		compactor, ok := driver.(external.Compactor)
		if !ok {
			return external.ErrControlUnsupported
		}
		delta, err := compactor.Compact(ctx, input)
		if err != nil {
			return err
		}
		_, err = s.sessionService.MergeRuntimeMetadata(ctx, sess.ID, sess.RuntimeType, delta)
		return err
	})
	return "", err
}

func (s *Service) runRuntimeControl(ctx context.Context, request RuntimeControlRequest, run func(context.Context, session.Thread, external.Driver, external.PromptInput) error) (resultErr error) {
	if s.sessionRuntime == nil {
		return external.ErrControlUnsupported
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	admission, err := s.sessionRuntime.Admit(runCtx, sessionruntime.AdmitInput{
		BotID: request.BotID, SessionID: request.ThreadID, InvocationID: uuid.NewString(), Payload: payload,
		Execution: sessionruntime.Execution{
			ConfigurationOnly: request.ModeID != "",
			Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
				return sessionruntime.RunAdmissionView{}, nil
			},
			Cancel: func() { cancel(context.Canceled) }, OwnershipCancel: cancel,
		},
	})
	if err != nil {
		return err
	}
	if !admission.Started {
		return sessionruntime.ErrSessionBusy
	}
	handle := admission.Handle
	runCtx = runtimefence.WithContext(runCtx, runtimefence.Fence{BotID: handle.BotID, SessionID: handle.SessionID, Token: handle.FencingToken})
	defer func() {
		status := sessionruntime.RunStatusCompleted
		if resultErr != nil {
			status = sessionruntime.RunStatusErrored
		}
		if errors.Is(resultErr, context.Canceled) || runCtx.Err() != nil {
			status = sessionruntime.RunStatusAborted
		}
		finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(runCtx), terminalWriteTimeout)
		defer finishCancel()
		if err := s.sessionRuntime.FinishRunWithErrorCode(finishCtx, handle, status, string(apperror.CodeOf(publicRuntimeControlError(resultErr)))); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	// Re-read after admission: a queued send or another mode change may have
	// won while this request was validating its initial snapshot.
	sess, driver, input, err := s.runtimeControlTarget(runCtx, request)
	if err != nil {
		return err
	}
	input.RunID = handle.RunID
	controlCtx, err := s.runtimeControlContext(runCtx, request)
	if err != nil {
		return err
	}
	return run(controlCtx, sess, driver, input)
}

func (s *Service) runtimeControlContext(ctx context.Context, request RuntimeControlRequest) (context.Context, error) {
	bound, found, err := s.resolveSessionWorkdirBinding(ctx, request.BotID, request.ThreadID)
	if err != nil {
		return ctx, err
	}
	if found {
		return workspace.WithWorkspaceTarget(ctx, bound.TargetID), nil
	}
	return ctx, nil
}

func publicRuntimeControlError(err error) error {
	if err == nil || apperror.CodeOf(err) != "" {
		return err
	}
	var feedback *agentfeedback.Error
	if errors.As(err, &feedback) || errors.Is(err, turn.ErrTeamNotServed) {
		return err
	}
	code := apperror.CodeRuntimeControlFailed
	switch {
	case errors.Is(err, context.Canceled):
		code = apperror.CodeRuntimeControlCancelled
	case errors.Is(err, approval.ErrForbidden):
		code = apperror.CodeRuntimeControlForbidden
	case errors.Is(err, sessionruntime.ErrSessionBusy):
		code = apperror.CodeSessionBusy
	case errors.Is(err, external.ErrControlUnsupported):
		code = apperror.CodeRuntimeControlUnsupported
	case errors.Is(err, external.ErrCommandUnavailable):
		code = apperror.CodeRuntimeControlCommandUnavailable
	case errors.Is(err, external.ErrModeUnavailable):
		code = apperror.CodeRuntimeControlModeUnavailable
	case errors.Is(err, external.ErrThreadUnavailable):
		code = apperror.CodeRuntimeControlThreadUnavailable
	case errors.Is(err, external.ErrAuthRequired):
		code = apperror.CodeExternalRuntimeAuthRequired
	}
	return apperror.Wrap(code, err, nil)
}

func (s *Service) ControlRuntimeGoal(ctx context.Context, request RuntimeControlRequest, action string) (err error) {
	defer func() { err = publicRuntimeControlError(err) }()
	if action != "pause" && action != "clear" {
		return apperror.New(apperror.CodeRuntimeControlRequestInvalid, nil)
	}
	_, driver, input, err := s.runtimeControlTarget(ctx, request)
	if err != nil {
		return err
	}
	provider, ok := driver.(external.GoalProvider)
	if !ok {
		return external.ErrControlUnsupported
	}
	ctx, err = s.runtimeControlContext(ctx, request)
	if err != nil {
		return err
	}
	return provider.ControlGoal(ctx, input, action)
}
