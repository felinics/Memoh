package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/bots"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/workspace"
)

type ToolApprovalResponseInput struct {
	ControlID              string
	BotID                  string
	ThreadID               string
	ActorChannelIdentityID string
	ActorUserID            string
	ApprovalID             string
	ExplicitID             string
	ReplyExternalMessageID string
	Decision               string
	// OptionID names the agent-provided permission option the decider picked;
	// empty means the plain binary decision.
	OptionID                   string
	Reason                     string
	ChatToken                  string
	SuppressActivePromptAttach bool
}

type CommittedToolApprovalResponse struct {
	request      toolapproval.Request
	input        ToolApprovalResponseInput
	runID        string
	isACP        bool
	activePrompt *acpActivePromptSubscription
	ackOnly      bool
}

func (s *Service) respondToolApproval(ctx context.Context, input ToolApprovalResponseInput, eventCh chan<- WSStreamEvent) error {
	committed, err := s.CommitToolApprovalResponse(ctx, input)
	if err != nil {
		return err
	}
	return s.ContinueCommittedToolApprovalResponse(ctx, committed, eventCh)
}

func (s *Service) CommitToolApprovalResponse(ctx context.Context, input ToolApprovalResponseInput) (CommittedToolApprovalResponse, error) {
	if s.toolApproval == nil {
		return CommittedToolApprovalResponse{}, errors.New("tool approval service not configured")
	}
	target, err := s.toolApproval.ResolveTarget(ctx, toolapproval.ResolveInput{
		BotID:                  input.BotID,
		SessionID:              input.ThreadID,
		ExplicitID:             firstNonEmpty(input.ExplicitID, input.ApprovalID),
		ReplyExternalMessageID: input.ReplyExternalMessageID,
	})
	if err != nil {
		return CommittedToolApprovalResponse{}, err
	}
	isACP, err := s.isACPToolApprovalSession(ctx, target.SessionID)
	if err != nil {
		return CommittedToolApprovalResponse{}, err
	}
	ctx = workspace.WithWorkspaceTarget(ctx, target.WorkspaceTargetID)
	if isACP {
		if err := s.authorizeACPToolApprovalResponse(ctx, target, input); err != nil {
			return CommittedToolApprovalResponse{}, err
		}
	} else if err := s.authorizeToolApprovalResponse(ctx, target, input); err != nil {
		return CommittedToolApprovalResponse{}, err
	}
	if isACP && !s.toolApproval.CanRespond(target) {
		if _, err := s.toolApproval.Reject(ctx, target.ID, "", "tool approval expired: the requesting tool call is no longer waiting"); err != nil && !errors.Is(err, toolapproval.ErrAlreadyDecided) {
			return CommittedToolApprovalResponse{}, err
		}
		return CommittedToolApprovalResponse{request: target, input: input, isACP: true, ackOnly: true}, nil
	}
	decision := strings.ToLower(strings.TrimSpace(input.Decision))
	optionID := input.OptionID
	if optionID == "" && len(target.Options) > 0 {
		// Older Web/Desktop clients only send approve/reject. Preserve that
		// contract without inventing a durable choice: a binary response maps
		// only to the agent's one-shot option. Session/always options must be
		// selected explicitly by a client that can show their semantics.
		optionID, err = legacyPermissionOptionID(target.Options, decision)
	}
	if err != nil {
		return CommittedToolApprovalResponse{}, err
	}
	var activePrompt *acpActivePromptSubscription
	if isACP && !input.SuppressActivePromptAttach {
		activePrompt, _ = s.subscribeACPActivePrompt(
			firstNonEmpty(target.BotID, input.BotID),
			firstNonEmpty(target.SessionID, input.ThreadID),
		)
	}
	if optionID != "" {
		// The decider picked one of the agent's own options. Its kind is
		// authoritative for approve-vs-reject; a contradicting decision label
		// is an error rather than a silent reinterpretation.
		option, ok := toolapproval.FindOption(target.Options, optionID)
		switch {
		case !ok:
			err = fmt.Errorf("%w: unknown option %q", toolapproval.ErrOptionUnavailable, optionID)
		case option.Approves():
			if decision != "" && decision != "approve" && decision != "approved" {
				err = fmt.Errorf("%w: decision %q does not match allow option %q", toolapproval.ErrOptionUnavailable, input.Decision, optionID)
				break
			}
			target, err = s.toolApproval.ApproveOption(ctx, target.ID, input.ActorChannelIdentityID, input.Reason, optionID)
		default:
			if decision != "" && decision != "reject" && decision != "rejected" {
				err = fmt.Errorf("%w: decision %q does not match reject option %q", toolapproval.ErrOptionUnavailable, input.Decision, optionID)
				break
			}
			target, err = s.toolApproval.RejectOption(ctx, target.ID, input.ActorChannelIdentityID, input.Reason, optionID)
		}
	} else {
		switch decision {
		case "approve", "approved":
			target, err = s.toolApproval.Approve(ctx, target.ID, input.ActorChannelIdentityID, input.Reason)
		case "reject", "rejected":
			target, err = s.toolApproval.Reject(ctx, target.ID, input.ActorChannelIdentityID, input.Reason)
		default:
			err = fmt.Errorf("unknown tool approval decision %q", input.Decision)
		}
	}
	if err != nil {
		if activePrompt != nil {
			activePrompt.release()
		}
		return CommittedToolApprovalResponse{}, err
	}
	return CommittedToolApprovalResponse{
		request:      target,
		input:        input,
		isACP:        isACP,
		activePrompt: activePrompt,
	}, nil
}

func legacyPermissionOptionID(options []toolapproval.PermissionOption, decision string) (string, error) {
	wantKind := ""
	switch decision {
	case "approve", "approved":
		wantKind = toolapproval.OptionKindAllowOnce
	case "reject", "rejected":
		wantKind = toolapproval.OptionKindRejectOnce
	default:
		// Preserve the existing unknown-decision error below.
		return "", nil
	}
	match := ""
	matches := 0
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option.Kind), wantKind) {
			matches++
			if matches > 1 {
				match = ""
				break
			}
			match = option.ID
		}
	}
	if matches == 1 {
		return match, nil
	}
	if wantKind == toolapproval.OptionKindRejectOnce {
		// A binary rejection is always safe. With no reject_once option — or
		// an ambiguous set of several — the ACP adapter converts the rejected
		// result to a cancelled outcome instead of guessing an option or
		// selecting a broader reject_always scope.
		return "", nil
	}
	if matches > 1 {
		return "", fmt.Errorf("%w: legacy %s matches more than one %s option", toolapproval.ErrOptionUnavailable, decision, wantKind)
	}
	// No allow_once option exists, so a binary approve cannot be honored:
	// Memoh never persists ACP permission grants on the user's behalf, and
	// every remaining allow option carries always/session persistence the
	// binary surface could not show. The decider gets an explicit error and
	// must approve from a surface that renders the agent's options.
	return "", fmt.Errorf("%w: legacy %s requires an agent-provided %s option", toolapproval.ErrOptionUnavailable, decision, wantKind)
}

func (s *Service) ContinueCommittedToolApprovalResponse(ctx context.Context, committed CommittedToolApprovalResponse, eventCh chan<- WSStreamEvent) error {
	return s.continueCommittedToolApprovalResponse(ctx, committed, nil, eventCh)
}

func (s *Service) continueCommittedToolApprovalResponse(
	ctx context.Context,
	committed CommittedToolApprovalResponse,
	lifecycle *continuationLifecycleResult,
	eventCh chan<- WSStreamEvent,
) error {
	target := committed.request
	if strings.TrimSpace(target.ID) == "" {
		return errors.New("committed tool approval response is missing its request")
	}
	if committed.ackOnly {
		return emitApprovalAck(ctx, eventCh)
	}
	if committed.isACP {
		if committed.activePrompt != nil {
			return forwardACPActivePrompt(ctx, committed.activePrompt, eventCh, acpActivePromptForwardOptions{
				SkipToolCallID: target.ToolCallID,
				SkipApprovalID: target.ID,
			})
		}
		return emitApprovalAck(ctx, eventCh)
	}

	ctx = workspace.WithWorkspaceTarget(ctx, target.WorkspaceTargetID)
	runID := runIDForChatRequest(committed.runID)
	var toolResult sdk.ToolResultPart
	switch target.Status {
	case toolapproval.StatusApproved:
		result, err := s.executeApprovedTool(ctx, target, committed.input, runID)
		if err != nil {
			return err
		}
		toolResult = result
	case toolapproval.StatusRejected:
		toolResult = sdk.ToolResultPart{
			ToolCallID: target.ToolCallID,
			ToolName:   target.ToolName,
			Result:     s.limitToolResultText(rejectedToolResultText(committed.input.Reason), target.ToolName),
			IsError:    true,
		}
	default:
		return fmt.Errorf("committed tool approval has unexpected status %q", target.Status)
	}
	return s.storeToolResultAndContinue(ctx, target, committed.input, toolResult, runID, lifecycle, eventCh)
}

func (s *Service) toolOutputLimit() contextlimit.ToolOutputLimit {
	limit := native.DefaultLimits().ToolOutputLimit()
	if s != nil && s.agent != nil {
		limit = s.agent.Limits().ToolOutputLimit()
	}
	return limit
}

func (s *Service) limitToolResultText(text, toolName string) string {
	limit := s.toolOutputLimit()
	return contextlimit.LimitString(text, "tool result ("+toolName+")", limit)
}

func (s *Service) limitToolResultValue(value any, toolName string) any {
	return contextlimit.LimitToolOutput(value, "tool result ("+toolName+")", s.toolOutputLimit())
}

func (s *Service) limitToolApprovalResult(result sdk.ToolApprovalResult, toolName string) sdk.ToolApprovalResult {
	if result.Decision == sdk.ToolApprovalDecisionRejected {
		result.Reason = s.limitToolResultText(result.Reason, toolName)
	}
	return result
}

func (s *Service) isACPToolApprovalSession(ctx context.Context, sessionID string) (bool, error) {
	if s == nil || s.sessionService == nil {
		return false, nil
	}
	sess, err := s.sessionService.Get(ctx, sessionID)
	if err != nil {
		return false, err
	}
	return sessionpkg.IsACPRuntime(sess), nil
}

func (s *Service) authorizeACPToolApprovalResponse(ctx context.Context, target toolapproval.Request, input ToolApprovalResponseInput) error {
	if s == nil || s.sessionService == nil {
		return errors.New("session service not configured")
	}
	sessionID := firstNonEmpty(target.SessionID, input.ThreadID)
	sess, err := s.sessionService.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if !sessionpkg.IsACPRuntime(sess) {
		return s.authorizeToolApprovalResponse(ctx, target, input)
	}
	botID := firstNonEmpty(target.BotID, input.BotID)
	if strings.TrimSpace(sess.BotID) != "" && strings.TrimSpace(botID) != "" && sess.BotID != botID {
		return toolapproval.ErrForbidden
	}
	if strings.TrimSpace(botID) == "" {
		botID = sess.BotID
	}
	target.BotID = botID
	actorID := firstNonEmpty(input.ActorUserID, input.ActorChannelIdentityID)
	if actorID == "" {
		return toolapproval.ErrForbidden
	}
	acpMeta := mergeACPRuntimeMetadata(sess.Metadata, sess.RuntimeMetadata)
	runtimeOwnerID := metadataString(acpMeta, "runtime_owner_account_id")
	if runtimeOwnerID == "" {
		return toolapproval.ErrForbidden
	}
	// The runtime owner has no standing beyond their live grants: a revoked
	// or offboarded owner must lose approval authority at decision time, so
	// every actor — owner included — passes the permission check.
	return s.authorizeToolApprovalResponse(ctx, target, input)
}

func (s *Service) authorizeToolApprovalResponse(ctx context.Context, target toolapproval.Request, input ToolApprovalResponseInput) error {
	if s == nil || s.botPermissions == nil {
		return errors.New("bot permission checker not configured")
	}
	botID := firstNonEmpty(target.BotID, input.BotID)
	// Channel deciders without a bound account carry only their channel
	// identity; grants are keyed on that identity, so it stays a valid
	// authorization subject (base behavior).
	actorID := firstNonEmpty(input.ActorUserID, input.ActorChannelIdentityID)
	permission, ok := toolApprovalPermission(target.Operation)
	if strings.TrimSpace(botID) == "" || strings.TrimSpace(actorID) == "" || !ok {
		return toolapproval.ErrForbidden
	}
	if ok, err := s.botPermissions.HasBotPermission(ctx, botID, actorID, permission); err != nil {
		return err
	} else if !ok {
		return toolapproval.ErrForbidden
	}
	return nil
}

func toolApprovalPermission(operation string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case toolapproval.OperationRead:
		return bots.PermissionWorkspaceRead, true
	case toolapproval.OperationWrite:
		return bots.PermissionWorkspaceWrite, true
	case toolapproval.OperationExec:
		return bots.PermissionWorkspaceExec, true
	case toolapproval.OperationPermission:
		// A generic agent permission request (network grant, mode switch)
		// authorizes the agent to act, so it sits at the same authority as
		// running the agent's own commands.
		return bots.PermissionWorkspaceExec, true
	default:
		return "", false
	}
}

func emitApprovalAck(ctx context.Context, eventCh chan<- WSStreamEvent) error {
	if eventCh == nil {
		return nil
	}
	for _, event := range []native.StreamEvent{
		{Type: native.EventAgentStart},
		{Type: native.EventAgentEnd},
	} {
		if err := sendAgentStreamEvent(ctx, eventCh, event); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) executeApprovedTool(ctx context.Context, req toolapproval.Request, input ToolApprovalResponseInput, runID string) (sdk.ToolResultPart, error) {
	ctx = workspace.WithWorkspaceTarget(ctx, req.WorkspaceTargetID)
	req = withLocalWebReplyTarget(req)
	resolved, err := s.ResolveRunConfig(ctx,
		input.BotID,
		req.SessionID,
		firstNonEmpty(req.ChannelIdentityID, input.ActorChannelIdentityID),
		req.SourcePlatform,
		req.ReplyTarget,
		req.ConversationType,
		input.ChatToken,
	)
	if err != nil {
		return sdk.ToolResultPart{}, err
	}
	resolved.RunConfig.RunID = runIDForChatRequest(runID)
	return s.agent.ExecuteTool(ctx, resolved.RunConfig, sdk.ToolCall{
		ToolCallID: req.ToolCallID,
		ToolName:   req.ToolName,
		Input:      req.ToolInput,
	})
}

func (s *Service) storeToolResultAndContinue(
	ctx context.Context,
	approval toolapproval.Request,
	input ToolApprovalResponseInput,
	result sdk.ToolResultPart,
	runID string,
	lifecycle *continuationLifecycleResult,
	eventCh chan<- WSStreamEvent,
) error {
	approval = withLocalWebReplyTarget(approval)
	ctx = workspace.WithWorkspaceTarget(ctx, approval.WorkspaceTargetID)
	target, err := s.resolveWorkspaceTargetSnapshot(ctx, input.BotID, approval.WorkspaceTargetID)
	if err != nil {
		return err
	}
	modelMessages := sdkMessagesToModelMessages([]sdk.Message{sdk.ToolMessage(result)})
	storeReq := ChatRequest{
		RunID:                   runID,
		BotID:                   input.BotID,
		ChatID:                  input.BotID,
		ThreadID:                approval.SessionID,
		SourceChannelIdentityID: firstNonEmpty(approval.ChannelIdentityID, input.ActorChannelIdentityID),
		CurrentChannel:          approval.SourcePlatform,
		ReplyTarget:             approval.ReplyTarget,
		ConversationType:        approval.ConversationType,
		UserMessagePersisted:    true,
		WorkspaceTargetID:       approval.WorkspaceTargetID,
	}
	storeReq.WorkspaceTarget = target
	if err := s.storeRoundWithOptions(ctx, storeReq, modelMessages, "", storeRoundOptions{AllowPendingToolCalls: true}); err != nil {
		return err
	}
	return s.continueToolApprovalSession(ctx, approval, input, runID, lifecycle, eventCh)
}

func (s *Service) continueToolApprovalSession(
	ctx context.Context,
	approval toolapproval.Request,
	input ToolApprovalResponseInput,
	runID string,
	runtimeLifecycle *continuationLifecycleResult,
	eventCh chan<- WSStreamEvent,
) error {
	approval = withLocalWebReplyTarget(approval)
	ctx = workspace.WithWorkspaceTarget(ctx, approval.WorkspaceTargetID)
	resolved, err := s.ResolveRunConfig(ctx,
		input.BotID,
		approval.SessionID,
		firstNonEmpty(approval.ChannelIdentityID, input.ActorChannelIdentityID),
		approval.SourcePlatform,
		approval.ReplyTarget,
		approval.ConversationType,
		input.ChatToken,
	)
	if err != nil {
		return err
	}
	resolved.RunConfig.RunID = runIDForChatRequest(runID)

	cfg, err := s.prepareContinuationRunConfig(
		ctx,
		resolved.RunConfig,
		historyScopeFallbackFromToolApprovalRequest(approval),
		compactionSummaryScope(firstNonEmpty(approval.BotID, input.BotID), "", approval.SessionID, approval.ConversationType, "", approval.ReplyTarget),
		eventCh,
	)
	if err != nil {
		return err
	}
	terminal := s.contextLifecycleTerminal(ctx, cfg)
	var lifecycleCause error
	var lifecycleDeferred bool
	var terminalEventSeen bool
	defer func() {
		if runtimeLifecycle != nil {
			runtimeLifecycle.cause = lifecycleCause
			runtimeLifecycle.deferred = lifecycleDeferred
			if snapshot, ok := cfg.ContextLifecycle.Snapshot(); ok {
				runtimeLifecycle.snapshot = &snapshot
			}
			return
		}
		if !lifecycleDeferred {
			terminal(lifecycleCause)
		}
	}()

	req := ChatRequest{
		RunID:                   cfg.RunID,
		BotID:                   input.BotID,
		ChatID:                  input.BotID,
		ThreadID:                approval.SessionID,
		SourceChannelIdentityID: firstNonEmpty(approval.ChannelIdentityID, input.ActorChannelIdentityID),
		CurrentChannel:          approval.SourcePlatform,
		ReplyTarget:             approval.ReplyTarget,
		ConversationType:        approval.ConversationType,
		UserMessagePersisted:    true,
		WorkspaceTargetID:       approval.WorkspaceTargetID,
		WorkspaceTarget:         workspaceTargetFromRunConfig(resolved.RunConfig),
	}

	reasoningTiming := newReasoningTimingTracker(nil)
	configureNativeReasoningTiming(&cfg, reasoningTiming, nil)
	idleCtx, idleCancel := s.withStreamIdleTimeout(ctx, reasoningEffortForIdle(cfg))
	defer idleCancel.Stop()
	stream := s.agent.Stream(idleCtx, cfg)
	stored := false
	failureEventForwarded := false
	var hasVisibleOutput bool
	for event := range stream {
		idleCancel.Reset()
		if event.Type == native.EventToolCallStart {
			idleCancel.RecordToolCall()
		}
		if eventErr := agentStreamLifecycleError(event); eventErr != nil && lifecycleCause == nil {
			lifecycleCause = eventErr
		}
		if event.IsTerminal() {
			terminalEventSeen = true
			lifecycleDeferred = pendingContinuationDecision(event)
			if !lifecycleDeferred {
				switch event.Type {
				case native.EventAgentEnd:
					lifecycleCause = nil
				case native.EventAgentAbort:
					if idleCancel.DidFire() {
						lifecycleCause = context.Cause(idleCtx)
					} else if context.Cause(ctx) != nil || lifecycleCause == nil {
						lifecycleCause = agentAbortCause(ctx)
					}
				}
			}
		}
		if hasVisibleAgentStreamOutput(event) {
			hasVisibleOutput = true
		}
		if event.Type == native.EventAgentAbort && idleCancel.DidFire() && eventCh != nil {
			if failureData, marshalErr := json.Marshal(agentFailureStreamEvent(context.Cause(idleCtx))); marshalErr == nil {
				select {
				case eventCh <- json.RawMessage(failureData):
					failureEventForwarded = true
				case <-ctx.Done():
					lifecycleCause = context.Cause(ctx)
					return lifecycleCause
				}
			}
		}
		data, err := json.Marshal(publicAgentStreamEvent(event))
		if err != nil {
			continue
		}
		if !stored && event.IsTerminal() && len(event.Messages) > 0 {
			if snap, ok := extractTerminalSnapshot(data); ok {
				snap.reasoningTiming = takeTerminalReasoningTiming(reasoningTiming, event.Type)
				snap.visibleOutput = hasVisibleOutput
				snap.failureCode = snapshotFailureCode(idleCancel.DidFire(), lifecycleCause)
				lifecycleDeferred = lifecycleDeferred || snap.deferredToolID != ""
				if snap.aborted && !lifecycleDeferred && lifecycleCause == nil {
					lifecycleCause = agentAbortCause(ctx)
				}
				if storeErr := s.persistTerminalSnapshot(
					context.WithoutCancel(ctx),
					req,
					resolvedContext{runConfig: cfg, model: models.GetResponse{ID: resolved.ModelID}},
					snap,
				); storeErr != nil {
					lifecycleCause = storeErr
					lifecycleDeferred = false
					return storeErr
				}
				stored = true
			}
		}
		if eventCh != nil && shouldForwardAfterIdleFailure(event, failureEventForwarded) {
			select {
			case eventCh <- json.RawMessage(data):
			case <-ctx.Done():
				lifecycleCause = context.Cause(ctx)
				return lifecycleCause
			}
		}
	}
	if idleCancel.DidFire() {
		lifecycleCause = context.Cause(idleCtx)
		if !stored {
			if _, storeErr := s.persistTurnFailure(context.WithoutCancel(ctx), req, resolvedContext{runConfig: cfg, model: models.GetResponse{ID: resolved.ModelID}}, snapshotFailureCode(true, lifecycleCause)); storeErr != nil {
				s.logger.Error("tool approval timeout persist failed", slog.Any("error", storeErr))
			}
		}
		if eventCh != nil && !failureEventForwarded {
			if data, marshalErr := json.Marshal(agentFailureStreamEvent(lifecycleCause)); marshalErr == nil {
				select {
				case eventCh <- json.RawMessage(data):
				case <-ctx.Done():
				}
			}
		}
		return lifecycleCause
	}
	if ctx.Err() != nil {
		lifecycleCause = context.Cause(ctx)
		return lifecycleCause
	}
	if lifecycleCause == nil && !lifecycleDeferred && !terminalEventSeen {
		lifecycleCause = errors.New("agent continuation ended without a terminal event")
	}
	return nil
}

func withLocalWebReplyTarget(req toolapproval.Request) toolapproval.Request {
	if strings.EqualFold(strings.TrimSpace(req.SourcePlatform), "web") && strings.TrimSpace(req.ReplyTarget) == "" {
		req.ReplyTarget = strings.TrimSpace(req.BotID)
	}
	return req
}

func rejectedToolResultText(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "tool execution rejected by user"
	}
	return "tool execution rejected by user: " + reason
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
