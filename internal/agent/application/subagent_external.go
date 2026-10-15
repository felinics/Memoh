package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/runtimekind"
	"github.com/felinics/memoh/internal/workdir"
	"github.com/felinics/memoh/internal/workspace"
)

type subagentBackendCatalog interface {
	List(context.Context, string) ([]botagents.BotAgent, error)
	GetActive(context.Context, string, string) (botagents.BotAgent, error)
	ValidateConfiguration(context.Context, botagents.BotAgent, map[string]any) error
}

func (s *Service) externalSubagentExecutionGate(ctx context.Context, sess session.Thread) error {
	if sess.SessionMode != session.TypeSubagent {
		return nil
	}
	meta := sess.RuntimeMetadata
	if metadataString(meta, "external_execution_run") == "" && metadataString(meta, "external_termination_unconfirmed") == "" {
		return nil
	}
	_, err := s.settledSubagent(ctx, sess)
	return err
}

// settledSubagent confirms the child's earlier run stopped where it can and
// returns the child as now recorded, refusing while that run stays unknown.
func (s *Service) settledSubagent(ctx context.Context, sess session.Thread) (session.Thread, error) {
	latest, state, err := s.subagentStateSnapshot(ctx, sess)
	if err != nil {
		return sess, err
	}
	if state.Status == "execution_unknown" {
		return sess, apperror.New(apperror.CodeExternalRuntimeTerminationUnconfirmed, nil)
	}
	return latest, nil
}

func (s *Service) SetSubagentBackends(catalog subagentBackendCatalog) {
	s.subagentBackends = catalog
}

func (s *Service) subagentParent(ctx context.Context, parent tools.SessionContext) (session.Thread, error) {
	if s.sessionService == nil || parent.IsSubagent || parent.SessionType == session.TypeSubagent || parent.SessionID == "" || parent.BotID == "" {
		return session.Thread{}, apperror.New(apperror.CodeNoWorkspaceExec, nil)
	}
	sess, err := s.sessionService.Get(ctx, parent.SessionID)
	if err != nil {
		return session.Thread{}, err
	}
	if sess.BotID != parent.BotID || sess.Type == session.TypeSubagent || sess.SessionMode == session.TypeSubagent {
		return session.Thread{}, apperror.New(apperror.CodeNoWorkspaceExec, nil)
	}
	if err := s.requireRuntimeOwnerWorkspaceExec(ctx, parent.BotID, parent.UserID); err != nil {
		return session.Thread{}, err
	}
	return sess, nil
}

func (s *Service) externalSubagentBackend(ctx context.Context, botID, id string) (botagents.BotAgent, error) {
	if s.subagentBackends == nil || strings.TrimSpace(id) == "" {
		return botagents.BotAgent{}, apperror.New(apperror.CodeBotAgentUnavailable, nil)
	}
	agent, err := s.subagentBackends.GetActive(ctx, botID, id)
	if err != nil {
		return botagents.BotAgent{}, apperror.Wrap(apperror.CodeBotAgentUnavailable, err, nil)
	}
	if !runtimekind.IsExternal(botagents.SessionRuntime(agent.Runtime)) || s.externalDrivers[botagents.SessionRuntime(agent.Runtime)] == nil {
		return botagents.BotAgent{}, apperror.New(apperror.CodeBotAgentUnavailable, nil)
	}
	var metadata map[string]any
	if s.queries != nil {
		botUUID, err := db.ParseUUID(botID)
		if err != nil {
			return botagents.BotAgent{}, err
		}
		bot, err := s.queries.GetBotByID(ctx, botUUID)
		if err != nil {
			return botagents.BotAgent{}, err
		}
		if len(bot.Metadata) > 0 {
			if err := json.Unmarshal(bot.Metadata, &metadata); err != nil {
				return botagents.BotAgent{}, err
			}
		}
	}
	if err := s.subagentBackends.ValidateConfiguration(ctx, agent, metadata); err != nil {
		return botagents.BotAgent{}, apperror.Wrap(apperror.CodeBotAgentInvalidMetadata, err, nil)
	}
	return agent, nil
}

// subagentToolError answers a failed delegation tool call with its public
// code. The model reads tool errors verbatim, so a dependency's diagnostic
// goes to the log, never into the result.
func (s *Service) subagentToolError(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}
	public, fault := errs.Answer(ctx, err)
	if fault == apperror.FaultServer || fault == apperror.FaultDependency {
		result := errlog.Event(ctx, operation, err, errlog.Options{})
		s.logger.LogAttrs(ctx, result.Level, "subagent delegation failed", result.Attrs()...)
	}
	return public
}

func (s *Service) ListSubagentBackends(ctx context.Context, parent tools.SessionContext) ([]tools.SubagentBackend, error) {
	items, err := s.listSubagentBackends(ctx, parent)
	return items, s.subagentToolError(ctx, "agent.subagent_backends", err)
}

func (s *Service) listSubagentBackends(ctx context.Context, parent tools.SessionContext) ([]tools.SubagentBackend, error) {
	if _, err := s.subagentParent(ctx, parent); err != nil {
		return nil, err
	}
	if s.subagentBackends == nil {
		return nil, apperror.New(apperror.CodeBotAgentUnavailable, nil)
	}
	agents, err := s.subagentBackends.List(ctx, parent.BotID)
	if err != nil {
		return nil, err
	}
	items := make([]tools.SubagentBackend, 0, len(agents))
	for _, agent := range agents {
		item := tools.SubagentBackend{BotAgentID: agent.ID, Name: agent.Name, RuntimeType: botagents.SessionRuntime(agent.Runtime)}
		_, err := s.externalSubagentBackend(ctx, parent.BotID, agent.ID)
		item.Available = err == nil
		if err != nil {
			item.Code = string(apperror.CodeOf(s.subagentToolError(ctx, "agent.subagent_backends", err)))
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Service) PrepareExternalSubagent(ctx context.Context, parent tools.SessionContext, id string) (session.CreateInput, error) {
	input, err := s.prepareExternalSubagent(ctx, parent, id)
	return input, s.subagentToolError(ctx, "agent.subagent_prepare", err)
}

func nativeTurnTarget(parent tools.SessionContext) error {
	if (parent.WorkspaceTargetKind != "" && parent.WorkspaceTargetKind != workdir.TargetKindNative) || (parent.WorkspaceTargetID != "" && parent.WorkspaceTargetID != workspace.WorkspaceTargetNative) {
		return apperror.New(apperror.CodeExternalAgentContainerWorkspaceRequired, nil)
	}
	return nil
}

func (s *Service) nativeSessionWorkdir(ctx context.Context, botID, sessionID string) (workdir.Resolved, bool, error) {
	resolved, bound, err := s.resolveSessionWorkdirBinding(ctx, botID, sessionID)
	if err == nil && bound && resolved.Kind != workdir.TargetKindNative {
		err = apperror.New(apperror.CodeExternalAgentContainerWorkspaceRequired, nil)
	}
	return resolved, bound, err
}

func (s *Service) prepareExternalSubagent(ctx context.Context, parent tools.SessionContext, id string) (session.CreateInput, error) {
	if err := nativeTurnTarget(parent); err != nil {
		return session.CreateInput{}, err
	}
	sess, err := s.subagentParent(ctx, parent)
	if err != nil {
		return session.CreateInput{}, err
	}
	agent, err := s.externalSubagentBackend(ctx, parent.BotID, id)
	if err != nil {
		return session.CreateInput{}, err
	}
	descriptor, err := botagents.DescriptorFor(agent)
	if err != nil {
		return session.CreateInput{}, err
	}
	projectPath := firstNonEmpty(parent.WorkdirPath, metadataString(runtimeSessionMeta(sess), "project_path"), "/data")
	if resolved, bound, err := s.nativeSessionWorkdir(ctx, parent.BotID, sess.ID); err != nil {
		return session.CreateInput{}, err
	} else if bound {
		projectPath = resolved.WorkDir
	}
	meta := map[string]any{"runtime_owner_account_id": parent.UserID, "project_path": projectPath}
	if descriptor.Provider != "" {
		meta["acp_agent_id"] = descriptor.Provider
	}
	return session.CreateInput{BotID: parent.BotID, ParentThreadID: parent.SessionID, CreatedByUserID: parent.UserID, RuntimeType: botagents.SessionRuntime(agent.Runtime), BotAgentID: agent.ID, WorkdirID: sess.WorkdirID, WorkdirPath: projectPath, RuntimeMetadata: meta}, nil
}

func (s *Service) RunExternalSubagent(ctx context.Context, input tools.ExternalSubagentRun) (*tools.SpawnResult, error) {
	if err := nativeTurnTarget(input.Parent); err != nil {
		return nil, err
	}
	if _, err := s.subagentParent(ctx, input.Parent); err != nil {
		return nil, err
	}
	sess, err := s.sessionService.Get(ctx, input.SessionID)
	if err != nil {
		return nil, err
	}
	if sess.BotID != input.Parent.BotID || sess.ParentThreadID != input.Parent.SessionID || sess.SessionMode != session.TypeSubagent || sess.BotAgentID != input.BotAgentID || sess.RuntimeType != input.RuntimeType {
		return nil, apperror.New(apperror.CodeBotAgentUnavailable, nil)
	}
	if _, _, err := s.nativeSessionWorkdir(ctx, sess.BotID, sess.ID); err != nil {
		return nil, err
	}
	agent, err := s.externalSubagentBackend(ctx, sess.BotID, sess.BotAgentID)
	if err != nil {
		return nil, err
	}
	if botagents.SessionRuntime(agent.Runtime) != sess.RuntimeType {
		return nil, apperror.New(apperror.CodeBotAgentUnavailable, nil)
	}
	handle, ok := SubagentRunHandleFromContext(ctx)
	if !ok || handle.RunID != input.Admission.RunID || handle.SessionID != sess.ID || handle.BotID != sess.BotID || handle.FencingToken <= 0 {
		return nil, errors.New("external subagent requires its admitted run")
	}
	req := ChatRequest{
		BotID: sess.BotID, ChatID: firstNonEmpty(input.Parent.ChatID, sess.BotID), ThreadID: sess.ID,
		RunID: handle.RunID, RunHandle: handle, TurnID: input.Admission.TurnID, TurnPosition: &input.Admission.TurnPosition,
		Query: input.Query, RawQuery: input.Query, UserID: input.Parent.UserID, SourceChannelIdentityID: input.Parent.ChannelIdentityID,
		ChatToken: input.Parent.SessionToken, CurrentChannel: input.Parent.CurrentPlatform, ReplyTarget: input.Parent.ReplyTarget,
		ConversationType: input.Parent.ConversationType, WorkspaceTargetID: input.Parent.WorkspaceTargetID,
		SessionType: session.TypeSubagent, RuntimeType: sess.RuntimeType, SkipMemoryExtraction: true, SkipTitleGeneration: true,
		RuntimeCanRequestUserInput: &input.Parent.CanRequestUserInput,
	}
	if err := s.recordRunResumeContext(ctx, req); err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan WSStreamEvent, 16)
	type completion struct {
		outcome RunOutcome
		err     error
	}
	done := make(chan completion, 1)
	go func() {
		outcome, err := s.streamRuntimeWS(streamCtx, s.externalDrivers[sess.RuntimeType], req, events, nil, false)
		done <- completion{outcome, err}
		close(events)
	}()
	result := &tools.SpawnResult{Persisted: true}
	observer := s.SubagentRunObserver(ctx)
	var terminal native.StreamEventType
	var streamErr error
	for raw := range events {
		var event native.StreamEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			streamErr = err
			cancel()
			continue
		}
		if event.Type == native.EventError {
			streamErr = agentStreamFailure(event)
		}
		if event.Type == native.EventEnd || event.Type == native.EventAbort {
			terminal = event.Type
		}
		if len(event.Messages) > 0 || len(event.Usage) > 0 {
			if len(event.Messages) > 0 {
				if err := json.Unmarshal(event.Messages, &result.Messages); err != nil {
					streamErr = err
				}
			}
			if len(event.Usage) > 0 {
				_ = json.Unmarshal(event.Usage, &result.Usage)
			}
		}
		if observer != nil {
			observed := observer(event)
			if observed.TerminalResolved {
				result.TerminalResolved = true
				result.TerminalOutcome = observed.TerminalOutcome
			}
		}
	}
	completed := <-done
	result.ExecutionUnconfirmed = completed.outcome.ExecutionUnconfirmed
	if apperror.CodeOf(completed.err) == apperror.CodeExternalRuntimeTerminationUnconfirmed {
		result.TerminalResolved = true
		result.TerminalOutcome = tools.SpawnAttemptFailure
	}
	const reportLimit = 16 * 1024
	var report strings.Builder
	truncated := false
	for _, message := range result.Messages {
		if message.Role != sdk.MessageRoleAssistant {
			continue
		}
		hasText := false
		for _, part := range message.Content {
			if text, ok := part.(sdk.TextPart); ok && text.Text != "" {
				hasText = true
				break
			}
		}
		if !hasText {
			continue
		}
		report.Reset()
		truncated = false
		for _, part := range message.Content {
			if text, ok := part.(sdk.TextPart); ok {
				remaining := reportLimit - report.Len()
				if len(text.Text) > remaining {
					truncated = true
					report.WriteString(text.Text[:remaining])
				} else {
					report.WriteString(text.Text)
				}
			}
		}
	}
	result.Text = report.String()
	if truncated {
		suffix := "\n[Report truncated. Full transcript: " + sess.ID + "]"
		result.Text = result.Text[:reportLimit-len(suffix)]
		for !utf8.ValidString(result.Text) {
			result.Text = result.Text[:len(result.Text)-1]
		}
		result.Text += suffix
	}
	result.Messages = nil
	if completed.err != nil {
		return result, completed.err
	}
	if completed.outcome.Cause != nil {
		return result, completed.outcome.Cause
	}
	if streamErr != nil {
		return result, streamErr
	}
	if result.TerminalResolved && result.TerminalOutcome == tools.SpawnAttemptAbort {
		return result, context.Canceled
	}
	if result.TerminalResolved && result.TerminalOutcome == tools.SpawnAttemptFailure {
		return result, apperror.New(apperror.CodeRuntimePromptFailed, nil)
	}
	if terminal != native.EventEnd {
		return result, apperror.New(apperror.CodeRuntimePromptFailed, nil)
	}
	return result, nil
}

type externalExecutionStore interface {
	ListExternalExecutionHolds(ctx context.Context, botID string) ([]session.Thread, error)
	ClaimExternalSubagentReap(ctx context.Context, sessionID, runID string, lease time.Duration) (string, bool, error)
	ResolveExternalSubagentTermination(ctx context.Context, sessionID, runID, claim string) (session.Thread, bool, error)
}

func (s *Service) externalExecutionHolds(ctx context.Context, botID string) ([]session.Thread, error) {
	store, ok := s.sessionService.(externalExecutionStore)
	if !ok {
		return nil, errs.New("external subagent execution records unavailable")
	}
	return store.ListExternalExecutionHolds(ctx, botID)
}

// HeldExternalSubagents names the bot's external children whose durable
// execution record is unresolved: running, or ended without confirmed
// termination. It only reads; SettleExternalSubagents resolves records.
func (s *Service) HeldExternalSubagents(ctx context.Context, botID string) ([]string, error) {
	children, err := s.externalExecutionHolds(ctx, botID)
	if err != nil {
		return nil, s.subagentToolError(ctx, "agent.subagent_capacity", err)
	}
	held := make([]string, 0, len(children))
	for _, child := range children {
		held = append(held, child.ID)
	}
	return held, nil
}

// SettleExternalSubagents confirms the termination of every child of the bot
// whose last run ended unconfirmed and whose leftovers can be stopped now.
func (s *Service) SettleExternalSubagents(ctx context.Context, botID string) error {
	children, err := s.externalExecutionHolds(ctx, botID)
	for _, child := range children {
		if err != nil {
			break
		}
		if _, _, err = s.subagentStateSnapshot(ctx, child); apperror.CodeOf(err) == apperror.CodeSessionBusy {
			err = nil
		}
	}
	return s.subagentToolError(ctx, "agent.subagent_capacity", err)
}
