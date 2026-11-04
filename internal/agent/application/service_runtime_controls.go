package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/decision/approval"
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
	driver, _ := s.runtimes.Driver(session.RuntimeTypeOf(sess))
	return sess, driver, runtimeControlInput(sess, request), nil
}

func runtimeControlInput(sess session.Thread, request RuntimeControlRequest) external.PromptInput {
	meta := runtimeSessionMeta(sess)
	input := external.PromptInput{
		BotID: sess.BotID, BotAgentID: sess.BotAgentID, ThreadID: sess.ID,
		RuntimeMetadata: meta, Command: request.Command,
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
	// Command discovery runs a CLI in the workspace the session's turns use.
	ctx, err = s.runtimeControlContext(ctx, request)
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
	if provider, ok := external.As[external.CommandProvider](s.runtimes, session.RuntimeTypeOf(sess)); ok {
		ctx, err = s.runtimeControlContext(ctx, request)
		if err != nil {
			return nil, err
		}
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
	provider, ok := external.As[external.GoalProvider](s.runtimes, runtimeTypeOf(driver))
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
	if _, _, _, err := runtimeModeAccessors(s.runtimes, driver, request.ModeKind); err != nil {
		return out, err
	}

	err = s.runRuntimeControl(ctx, request, true, func(ctx context.Context, sess session.Thread, driver external.Driver, input external.PromptInput) error {
		read, set, metadataKey, err := runtimeModeAccessors(s.runtimes, driver, request.ModeKind)
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
			return external.Fail(external.FailureModeUnavailable, nil)
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

func runtimeModeAccessors(runtimes *external.Registry, driver external.Driver, kind string) (readRuntimeMode, setRuntimeMode, string, error) {
	switch kind {
	case "", "permission":
		if provider, ok := external.As[external.ModeProvider](runtimes, runtimeTypeOf(driver)); ok {
			return provider.Modes, provider.SetMode, "permission_mode", nil
		}
	case "plan":
		if provider, ok := external.As[external.PlanModeProvider](runtimes, runtimeTypeOf(driver)); ok {
			return provider.PlanMode, provider.SetPlanMode, "collaboration_mode", nil
		}
	default:
		return nil, nil, "", apperror.New(apperror.CodeRuntimeControlRequestInvalid, nil)
	}
	return nil, nil, "", external.ErrControlUnsupported
}

// ExecuteRuntimeCommand handles commands which do not create a conversation
// turn. Turn commands go through the existing chat admission/persistence path.
func (s *Service) ExecuteRuntimeCommand(ctx context.Context, request RuntimeControlRequest) (result turn.RuntimeCommandResult, resultErr error) {
	defer func() { resultErr = publicRuntimeControlError(resultErr) }()
	_, driver, input, err := s.runtimeControlTarget(ctx, request)
	if err != nil {
		return turn.RuntimeCommandResult{}, err
	}
	provider, ok := external.As[external.CommandProvider](s.runtimes, runtimeTypeOf(driver))
	if !ok {
		return turn.RuntimeCommandResult{}, external.ErrCommandUnavailable
	}
	controlCtx, err := s.runtimeControlContext(ctx, request)
	if err != nil {
		return turn.RuntimeCommandResult{}, err
	}
	commands, err := provider.Commands(controlCtx, input)
	if err != nil {
		return turn.RuntimeCommandResult{}, err
	}
	command, ok := external.FindCommand(commands, request.Command)
	if !ok {
		return turn.RuntimeCommandResult{}, external.ErrCommandUnavailable
	}
	if command.Kind == external.CommandRead && !driver.Caps().ReadCommandsRequireIdle {
		return provider.ReadCommand(controlCtx, input)
	}
	if command.Kind != external.CommandOperation && command.Kind != external.CommandRead {
		return turn.RuntimeCommandResult{}, external.ErrCommandUnavailable
	}
	// A read only holds the slot so it cannot meet a turn on the native
	// session; it is no assistant turn, and must not be shown as one.
	err = s.runRuntimeControl(ctx, request, command.Kind == external.CommandRead, func(ctx context.Context, sess session.Thread, driver external.Driver, input external.PromptInput) error {
		provider, ok := external.As[external.CommandProvider](s.runtimes, runtimeTypeOf(driver))
		if !ok {
			return external.ErrCommandUnavailable
		}
		commands, err := provider.Commands(ctx, input)
		if err != nil {
			return err
		}
		current, ok := external.FindCommand(commands, request.Command)
		if !ok || (current.Kind != external.CommandOperation && current.Kind != external.CommandRead) {
			return external.ErrCommandUnavailable
		}
		if current.Kind == external.CommandRead {
			result, err = provider.ReadCommand(ctx, input)
			return err
		}
		compactor, ok := external.As[external.Compactor](s.runtimes, runtimeTypeOf(driver))
		if !ok {
			return external.ErrControlUnsupported
		}
		result, err := compactor.Compact(ctx, input)
		if err != nil {
			return err
		}
		_, err = s.sessionService.MergeRuntimeMetadata(ctx, sess.ID, sess.RuntimeType, result.RuntimeMetadata)
		return err
	})
	return result, err
}

func (s *Service) runRuntimeControl(ctx context.Context, request RuntimeControlRequest, configurationOnly bool, run func(context.Context, session.Thread, external.Driver, external.PromptInput) error) (resultErr error) {
	return s.runSessionControl(ctx, request, configurationOnly, func(runCtx context.Context, runID string) error {
		// Re-read after admission: a queued send or another mode change may have
		// won while this request was validating its initial snapshot.
		sess, driver, input, err := s.runtimeControlTarget(runCtx, request)
		if err != nil {
			return err
		}
		input.RunID = runID
		controlCtx, err := s.runtimeControlContext(runCtx, request)
		if err != nil {
			return err
		}
		return run(controlCtx, sess, driver, input)
	})
}

// runSessionControl shares the durable turn slot with non-chat operations.
// Authorization is performed by the caller; state must be re-read in run.
func (s *Service) runSessionControl(ctx context.Context, request RuntimeControlRequest, configurationOnly bool, run func(context.Context, string) error) (resultErr error) {
	if s == nil || s.sessionRuntime == nil {
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
			ConfigurationOnly: configurationOnly,
			Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
				return sessionruntime.RunAdmissionView{}, nil
			},
			Cancel: func() { cancel(context.Canceled) }, OwnershipCancel: cancel,
		},
	})
	// The session runtime is this adapter's port; callers outside it only see
	// the turn sentinel.
	if errors.Is(err, sessionruntime.ErrSessionBusy) {
		return turn.ErrSessionBusy
	}
	if err != nil {
		return err
	}
	if !admission.Started {
		return turn.ErrSessionBusy
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
		outcome := RunOutcome{Status: status, Cause: resultErr}
		finishCtx, finishCancel := context.WithTimeout(WithRunOutcome(context.WithoutCancel(runCtx), handle.RunID, outcome), terminalWriteTimeout)
		defer finishCancel()
		if _, err := s.sessionRuntime.FinishRunWithErrorCode(finishCtx, handle, status, string(apperror.CodeOf(publicRuntimeControlError(resultErr)))); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	return run(runCtx, handle.RunID)
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

// publicRuntimeControlError keeps the team routing sentinel intact for its
// own transports; everything else goes through the shared runtime control
// translation.
func publicRuntimeControlError(err error) error {
	if errors.Is(err, turn.ErrTeamNotServed) {
		return err
	}
	return RuntimeControlError(err)
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
	provider, ok := external.As[external.GoalProvider](s.runtimes, runtimeTypeOf(driver))
	if !ok {
		return external.ErrControlUnsupported
	}
	ctx, err = s.runtimeControlContext(ctx, request)
	if err != nil {
		return err
	}
	return provider.ControlGoal(ctx, input, action)
}

// runtimeTypeOf is the runtime a control target resolved to; empty for a
// session no external runtime serves.
func runtimeTypeOf(driver external.Driver) string {
	if driver == nil {
		return ""
	}
	return driver.RuntimeType()
}
