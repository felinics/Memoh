package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/bots"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
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
	request         toolapproval.Request
	input           ToolApprovalResponseInput
	runID           string
	runHandle       sessionruntime.RunHandle
	isExternalAgent bool
	activePrompt    *externalAgentActivePromptSubscription
	ackOnly         bool
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
	isExternalAgent, err := s.isExternalAgentToolApprovalSession(ctx, target.SessionID)
	if err != nil {
		return CommittedToolApprovalResponse{}, err
	}
	ctx = workspace.WithWorkspaceTarget(ctx, target.WorkspaceTargetID)
	if isExternalAgent {
		if err := s.authorizeExternalAgentToolApprovalResponse(ctx, target, input); err != nil {
			return CommittedToolApprovalResponse{}, err
		}
	} else if err := s.authorizeToolApprovalResponse(ctx, target, input); err != nil {
		return CommittedToolApprovalResponse{}, err
	}
	if isExternalAgent && !s.toolApproval.CanRespond(target) {
		if _, err := s.toolApproval.Reject(ctx, target.ID, "", "tool approval expired: the requesting tool call is no longer waiting"); err != nil && !errors.Is(err, toolapproval.ErrAlreadyDecided) {
			return CommittedToolApprovalResponse{}, err
		}
		return CommittedToolApprovalResponse{request: target, input: input, isExternalAgent: true, ackOnly: true}, nil
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
	var activePrompt *externalAgentActivePromptSubscription
	if isExternalAgent && !input.SuppressActivePromptAttach {
		activePrompt, _ = s.subscribeExternalAgentActivePrompt(
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
		request:         target,
		input:           input,
		isExternalAgent: isExternalAgent,
		activePrompt:    activePrompt,
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
	if committed.isExternalAgent {
		if committed.activePrompt != nil {
			return forwardExternalAgentActivePrompt(ctx, committed.activePrompt, eventCh, externalAgentActivePromptForwardOptions{
				SkipToolCallID: target.ToolCallID,
				SkipApprovalID: target.ID,
			})
		}
		return emitApprovalAck(ctx, eventCh)
	}

	ctx = workspace.WithWorkspaceTarget(ctx, target.WorkspaceTargetID)
	runID := runIDForChatRequest(committed.runID)
	var toolResult sdk.ToolResultPart
	var toolUIMetadata map[string]any
	switch target.Status {
	case toolapproval.StatusApproved:
		result, uiMetadata, err := s.executeApprovedTool(ctx, target, committed.input, runID)
		if err != nil {
			return err
		}
		toolResult = result
		toolUIMetadata = uiMetadata
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
	return s.storeToolResultAndContinue(ctx, target, committed.input, toolResult, toolUIMetadata, runID, committed.runHandle, lifecycle, eventCh)
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

func (s *Service) isExternalAgentToolApprovalSession(ctx context.Context, sessionID string) (bool, error) {
	if s == nil || s.sessionService == nil {
		return false, nil
	}
	sess, err := s.sessionService.Get(ctx, sessionID)
	if err != nil {
		return false, err
	}
	return sessionpkg.UsesDecisionWaiter(sess), nil
}

func (s *Service) authorizeExternalAgentToolApprovalResponse(ctx context.Context, target toolapproval.Request, input ToolApprovalResponseInput) error {
	if s == nil || s.sessionService == nil {
		return errors.New("session service not configured")
	}
	sessionID := firstNonEmpty(target.SessionID, input.ThreadID)
	sess, err := s.sessionService.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if !sessionpkg.UsesDecisionWaiter(sess) {
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
	runtimeOwnerID := metadataString(runtimeSessionMeta(sess), "runtime_owner_account_id")
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

func (s *Service) executeApprovedTool(ctx context.Context, req toolapproval.Request, input ToolApprovalResponseInput, runID string) (sdk.ToolResultPart, map[string]any, error) {
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
		return sdk.ToolResultPart{}, nil, err
	}
	resolved.RunConfig.RunID = runIDForChatRequest(runID)
	part, uiMetadata, err := s.agent.ExecuteToolWithUIMetadata(ctx, resolved.RunConfig, sdk.ToolCall{
		ToolCallID: req.ToolCallID,
		ToolName:   req.ToolName,
		Input:      req.ToolInput,
	})
	return part, uiMetadata, err
}

func (s *Service) storeToolResultAndContinue(
	ctx context.Context,
	approval toolapproval.Request,
	input ToolApprovalResponseInput,
	result sdk.ToolResultPart,
	uiMetadata map[string]any,
	runID string,
	runHandle sessionruntime.RunHandle,
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
	storeOpts := storeRoundOptions{AllowPendingToolCalls: true}
	// UI-only payloads stripped from the tool output (e.g. the edit diff) ride
	// the tool row's metadata — the model history reads only the content.
	if len(uiMetadata) > 0 {
		storeOpts.MessageMetadataByIndex = map[int]map[string]any{0: uiMetadata}
	}
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
	if err := s.storeRoundWithOptions(ctx, storeReq, modelMessages, "", storeOpts); err != nil {
		return err
	}
	return s.continueToolApprovalSession(ctx, approval, input, runID, runHandle, lifecycle, eventCh)
}

func (s *Service) continueToolApprovalSession(
	ctx context.Context,
	approval toolapproval.Request,
	input ToolApprovalResponseInput,
	runID string,
	runHandle sessionruntime.RunHandle,
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

	req := ChatRequest{
		RunID:                   cfg.RunID,
		RunHandle:               runHandle,
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

	return s.runNativeDecisionContinuation(ctx, req, cfg, resolved.ModelID, runtimeLifecycle, eventCh)
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
