package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/toolcontext"
)

type NativeToolSourceOptions struct {
	AllowAll        bool
	AllowTools      map[string]bool
	Approval        NativeToolApprovalService
	UserInput       NativeToolUserInputService
	ToolEvents      NativeToolEventSink
	ToolOutputLimit ToolOutputLimit
}

type NativeToolApprovalService interface {
	EvaluatePolicy(ctx context.Context, input toolapproval.CreatePendingInput) (toolapproval.Evaluation, error)
	CreatePending(ctx context.Context, input toolapproval.CreatePendingInput) (toolapproval.Request, error)
	Get(ctx context.Context, approvalID string) (toolapproval.Request, error)
	Reject(ctx context.Context, approvalID, actorID, reason string) (toolapproval.Request, error)
	WaitForDecision(ctx context.Context, approvalID string) (toolapproval.Request, error)
	RegisterWaiter(approvalID string) func()
}

type NativeToolUserInputService interface {
	CreatePending(ctx context.Context, input userinput.CreatePendingInput) (userinput.Request, error)
	Cancel(ctx context.Context, input userinput.CancelInput) (userinput.Request, error)
	WaitForRegisteredResponse(ctx context.Context, requestID string) (userinput.Request, error)
	// RegisterWaiter must be called before the pending request is announced
	// to users, or an instant answer can be misjudged as orphaned.
	RegisterWaiter(requestID string) func()
}

// NativeToolEventSink delivers tool lifecycle events into the live prompt
// stream of the calling runtime - the same channel tool_call_start travels -
// so attachments like pending user input land on the existing tool call block.
type NativeToolEventSink interface {
	AppendToolEvent(session mcp.ToolSessionContext, event mcp.ToolStreamEvent) bool
}

// NativeToolSource exposes Memoh-native ToolProvider tools through the MCP
// ToolSource interface used by ACP and external tool gateways.
type NativeToolSource struct {
	logger     *slog.Logger
	mu         sync.RWMutex
	providers  []ToolProvider
	allowAll   bool
	allow      map[string]struct{}
	approval   NativeToolApprovalService
	userInput  NativeToolUserInputService
	toolEvents NativeToolEventSink
	limit      ToolOutputLimit
}

type nativeLoadedTool struct {
	tool          sdk.Tool
	usageProvider ToolUsage
	usageID       int
}

func NewNativeToolSource(log *slog.Logger, providers []ToolProvider, opts NativeToolSourceOptions) *NativeToolSource {
	if log == nil {
		log = slog.Default()
	}
	allow := map[string]struct{}{}
	for name, enabled := range opts.AllowTools {
		if !enabled {
			continue
		}
		if normalized := strings.TrimSpace(name); normalized != "" {
			if builtIn, ok := lookupBuiltInToolName(normalized); ok {
				allow[builtIn.String()] = struct{}{}
			}
		}
	}
	source := &NativeToolSource{
		logger:     log.With(slog.String("tool_source", "native")),
		allowAll:   opts.AllowAll,
		allow:      allow,
		approval:   opts.Approval,
		userInput:  opts.UserInput,
		toolEvents: opts.ToolEvents,
		limit:      opts.ToolOutputLimit,
	}
	source.SetProviders(providers)
	return source
}

func (s *NativeToolSource) SetProviders(providers []ToolProvider) {
	if s == nil {
		return
	}
	filtered := make([]ToolProvider, 0, len(providers))
	for _, provider := range providers {
		if provider != nil {
			filtered = append(filtered, provider)
		}
	}
	s.mu.Lock()
	s.providers = filtered
	s.mu.Unlock()
}

func (s *NativeToolSource) ListTools(ctx context.Context, session mcp.ToolSessionContext) ([]mcp.ToolDescriptor, error) {
	tools := s.loadTools(ctx, session)
	if len(tools) == 0 {
		return []mcp.ToolDescriptor{}, nil
	}
	seen := map[string]struct{}{}
	visible := make([]nativeLoadedTool, 0, len(tools))
	for _, item := range tools {
		name := strings.TrimSpace(item.tool.Name)
		if name == "" || item.tool.Execute == nil || !s.allowed(name) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		item.tool.Name = name
		visible = append(visible, item)
	}
	available := availableFromLoadedTools(visible)
	usageApplied := map[int]bool{}
	descriptors := make([]mcp.ToolDescriptor, 0, len(visible))
	for _, item := range visible {
		tool := item.tool
		description := strings.TrimSpace(tool.Description)
		if item.usageProvider != nil && !usageApplied[item.usageID] {
			if usage := strings.TrimSpace(item.usageProvider.Usage(ctx, sessionFromMCP(session), available)); usage != "" {
				description = appendToolUsageDescription(description, usage)
			}
			usageApplied[item.usageID] = true
		}
		descriptors = append(descriptors, mcp.ToolDescriptor{
			Name:        tool.Name,
			Description: description,
			InputSchema: toolInputSchema(tool.Parameters),
		})
	}
	return descriptors, nil
}

func (s *NativeToolSource) CallTool(ctx context.Context, session mcp.ToolSessionContext, toolName string, arguments map[string]any) (map[string]any, error) {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" || !s.allowed(toolName) {
		return nil, mcp.ErrToolNotFound
	}
	tools := s.loadTools(ctx, session)
	for _, item := range tools {
		tool := item.tool
		if strings.TrimSpace(tool.Name) != toolName || tool.Execute == nil || !s.allowed(toolName) {
			continue
		}
		if arguments == nil {
			arguments = map[string]any{}
		}
		if toolName == ToolAskUser().String() {
			result, err := s.callAskUser(ctx, session, arguments)
			if err != nil {
				return nil, err
			}
			return s.limitMCPResult(toolName, result), nil
		}
		approval, err := s.requireApproval(ctx, session, toolName, arguments)
		if err != nil {
			return nil, err
		}
		if !approval.approved {
			return s.limitMCPResult(toolName, mcp.BuildToolErrorResult(approval.message)), nil
		}
		if err := toolcontext.ValidateRuntimeGuard(ctx, session); err != nil {
			return nil, err
		}
		result, err := tool.Execute(&sdk.ToolExecContext{
			Context:  ctx,
			ToolName: toolName,
		}, arguments)
		if err != nil {
			return nil, err
		}
		publicResult := publicNativeToolResult(result)
		return s.limitMCPResult(toolName, mcp.BuildToolSuccessResult(publicResult)), nil
	}
	return nil, mcp.ErrToolNotFound
}

func (s *NativeToolSource) limitMCPResult(toolName string, result map[string]any) map[string]any {
	limit := ToolOutputLimit{}
	if s != nil {
		limit = s.limit
	}
	return mcp.LimitToolResult(result, "tool result ("+toolName+")", limit)
}

func publicNativeToolResult(result any) any {
	switch value := result.(type) {
	case ReadMediaToolOutput:
		return value.Public
	case *ReadMediaToolOutput:
		if value == nil {
			return nil
		}
		return value.Public
	default:
		return result
	}
}

func (s *NativeToolSource) callAskUser(ctx context.Context, session mcp.ToolSessionContext, arguments map[string]any) (map[string]any, error) {
	if !session.CanRequestUserInput {
		return mcp.BuildToolErrorResult("user input cannot be delivered in this session"), nil
	}
	if err := userinput.ValidateAskUserInput(arguments); err != nil {
		return mcp.BuildToolSuccessResult(map[string]any{
			"status":      "invalid_arguments",
			"error":       err.Error(),
			"instruction": "Call " + toolRef(ToolAskUser()) + " again with a valid `questions` array. Every question needs `text` and a `kind` of single_select, multi_select, or text; select kinds need `options` with labels.",
		}), nil
	}
	if s == nil || s.userInput == nil {
		return mcp.BuildToolErrorResult("user input service is not configured"), nil
	}
	toolCallID := strings.TrimSpace(session.ToolCallID)
	if toolCallID == "" {
		toolCallID = "mcp-" + uuid.NewString()
	}
	// This request only has an in-process waiter. If the process exits before
	// the waiter can cancel it, the expiry guard prevents later answers from
	// being accepted with no consumer left to observe them.
	expiresAt := time.Now().Add(userinput.DefaultWaitTimeout + time.Minute)
	result, err := userinput.RunFlow(ctx, s.userInput, userinput.FlowRequest{
		Input: userinput.CreatePendingInput{
			BotID:                        session.BotID,
			SessionID:                    session.SessionID,
			RouteID:                      session.RouteID,
			ChannelIdentityID:            session.ChannelIdentityID,
			RequestedByChannelIdentityID: session.ChannelIdentityID,
			ToolCallID:                   toolCallID,
			ToolName:                     ToolAskUser().String(),
			Input:                        arguments,
			ProviderMetadata: map[string]any{
				"source":     userinput.ProviderSourceACPMCP,
				"runtime_id": session.RuntimeID,
				"run_id":     session.RunID,
			},
			SourcePlatform:   session.CurrentPlatform,
			ReplyTarget:      session.ReplyTarget,
			ConversationType: session.ConversationType,
			ExpiresAt:        &expiresAt,
		},
		ActorChannelIdentityID: session.ChannelIdentityID,
		Interactive:            session.CanRequestUserInput,
		WaitTimeout:            userinput.DefaultWaitTimeout,
		NonInteractiveReason:   "user input requested without an interactive stream",
		UndeliveredReason:      "user input request was not delivered to the interactive stream",
		TimeoutReason:          "user input timed out",
		AbortReason:            "user input aborted",
		Emit: func(req userinput.Request) bool {
			return s.emitUserInputRequest(session, req)
		},
	})
	if err != nil {
		return nil, err
	}
	req := result.Request
	if req.Status != userinput.StatusPending {
		if len(req.Result) > 0 {
			return mcp.BuildToolSuccessResult(req.Result), nil
		}
		return mcp.BuildToolErrorResult(ToolAskUser().String() + " request is no longer pending"), nil
	}
	return mcp.BuildToolErrorResult(ToolAskUser().String() + " request is still pending"), nil
}

type nativeApprovalResult struct {
	approved bool
	message  string
}

// requireApproval delegates approval policy and waiting to toolapproval.RunFlow.
func (s *NativeToolSource) requireApproval(ctx context.Context, session mcp.ToolSessionContext, toolName string, arguments map[string]any) (nativeApprovalResult, error) {
	if s == nil || s.approval == nil {
		return nativeApprovalResult{approved: true}, nil
	}
	toolCallID := strings.TrimSpace(session.ToolCallID)
	if toolCallID == "" {
		toolCallID = "mcp-" + uuid.NewString()
	}
	result, err := toolapproval.RunFlow(ctx, s.approval, toolapproval.FlowRequest{
		Input: toolapproval.CreatePendingInput{
			BotID:                        session.BotID,
			SessionID:                    session.SessionID,
			RouteID:                      session.RouteID,
			ChannelIdentityID:            session.ChannelIdentityID,
			WorkspaceTargetID:            session.WorkspaceTargetID,
			RequestedByChannelIdentityID: session.ChannelIdentityID,
			ToolCallID:                   toolCallID,
			ToolName:                     toolName,
			ToolInput:                    arguments,
			SourcePlatform:               session.CurrentPlatform,
			ReplyTarget:                  session.ReplyTarget,
			ConversationType:             session.ConversationType,
			WorkspaceTargeted:            isWorkspaceTargetToolName(toolName),
		},
		Interactive:       strings.TrimSpace(session.RunID) != "",
		UndeliveredReason: "tool approval request was not delivered to the interactive stream",
		RegisterWaiter:    s.approval.RegisterWaiter,
		Emit: func(req toolapproval.Request) bool {
			return s.emitToolApprovalRequest(session, req)
		},
	})
	if err != nil {
		return nativeApprovalResult{}, err
	}
	if result.Approved {
		return nativeApprovalResult{approved: true}, nil
	}
	return nativeApprovalResult{message: toolapproval.RejectionMessage(result)}, nil
}

func isWorkspaceTargetToolName(toolName string) bool {
	_, ok := toolapproval.OperationForTool(toolName)
	return ok
}

func (s *NativeToolSource) emitToolApprovalRequest(session mcp.ToolSessionContext, req toolapproval.Request) bool {
	if s == nil || s.toolEvents == nil {
		return false
	}
	delivered := s.toolEvents.AppendToolEvent(session, mcp.ToolStreamEvent{
		Type:       "tool_approval_request",
		ToolCallID: req.ToolCallID,
		ToolName:   req.ToolName,
		Input:      req.ToolInput,
		ApprovalID: req.ID,
		ShortID:    req.ShortID,
		Status:     toolapproval.NormalizedStatus(req.Status),
		Metadata: map[string]any{
			"approval": toolapproval.RequestMetadata(req),
		},
	})
	if !delivered && s.logger != nil {
		s.logger.Warn("tool approval request not delivered to prompt stream",
			slog.String("approval_id", req.ID),
			slog.String("run_id", session.RunID))
	}
	return delivered
}

// emitUserInputRequest delivers the pending question over the same tool event
// channel as the gateway's tool_call_start, so the stream converter attaches
// it to the existing tool call block - exactly like the in-process agent loop.
func (s *NativeToolSource) emitUserInputRequest(session mcp.ToolSessionContext, req userinput.Request) bool {
	if s == nil || s.toolEvents == nil {
		return false
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = userinput.StatusPending
	}
	delivered := s.toolEvents.AppendToolEvent(session, mcp.ToolStreamEvent{
		Type:        "user_input_request",
		ToolCallID:  req.ToolCallID,
		ToolName:    req.ToolName,
		Input:       req.Input,
		UserInputID: req.ID,
		ShortID:     req.ShortID,
		Status:      status,
		Metadata:    userinput.DeferredMetadata(req),
	})
	if !delivered && s.logger != nil {
		s.logger.Warn("user input request not delivered to prompt stream",
			slog.String("request_id", req.ID),
			slog.String("run_id", session.RunID))
	}
	return delivered
}

func (s *NativeToolSource) loadTools(ctx context.Context, session mcp.ToolSessionContext) []nativeLoadedTool {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	providers := append([]ToolProvider(nil), s.providers...)
	s.mu.RUnlock()
	toolSession := sessionFromMCP(session)
	var out []nativeLoadedTool
	for providerIndex, provider := range providers {
		providerTools, err := provider.Tools(ctx, toolSession)
		if err != nil {
			s.logger.Warn("native tool provider failed", slog.Any("error", err))
			continue
		}
		var usageProvider ToolUsage
		if len(providerTools) > 0 {
			if providerUsage, ok := provider.(ToolUsage); ok {
				usageProvider = providerUsage
			}
		}
		for _, tool := range providerTools {
			out = append(out, nativeLoadedTool{tool: tool, usageProvider: usageProvider, usageID: providerIndex})
		}
	}
	return out
}

func sessionFromMCP(session mcp.ToolSessionContext) SessionContext {
	return SessionContext{
		BotID:                    session.BotID,
		ChatID:                   firstNonEmpty(session.ChatID, session.BotID),
		SessionID:                session.SessionID,
		SessionType:              session.SessionType,
		ChannelIdentityID:        session.ChannelIdentityID,
		SessionToken:             session.SessionToken,
		CurrentPlatform:          session.CurrentPlatform,
		ReplyTarget:              session.ReplyTarget,
		ConversationType:         session.ConversationType,
		WorkspaceTargetID:        session.WorkspaceTargetID,
		WorkspaceTargetKind:      session.WorkspaceTargetKind,
		WorkspaceTargetName:      session.WorkspaceTargetName,
		WorkdirPath:              session.WorkdirPath,
		CanRequestUserInput:      session.CanRequestUserInput,
		CanListUserInput:         session.CanListUserInput,
		SupportsImageInput:       session.SupportsImageInput,
		SupportsFileInput:        session.SupportsFileInput,
		IsSubagent:               session.IsSubagent,
		ReasoningStoredEffort:    session.ReasoningStoredEffort,
		ReasoningRequestedEffort: session.ReasoningRequestedEffort,
	}
}

func availableFromLoadedTools(items []nativeLoadedTool) AvailableTools {
	sdkTools := make([]sdk.Tool, 0, len(items))
	for _, item := range items {
		sdkTools = append(sdkTools, item.tool)
	}
	return NewAvailableTools(sdkTools)
}

func appendToolUsageDescription(description, usage string) string {
	description = strings.TrimSpace(description)
	usage = strings.TrimSpace(usage)
	if usage == "" {
		return description
	}
	if description == "" {
		return usage
	}
	return description + "\n\n" + usage
}

func (s *NativeToolSource) allowed(name string) bool {
	if s == nil {
		return false
	}
	normalized := strings.TrimSpace(name)
	if normalized == "" {
		return false
	}
	if _, ok := s.allow[normalized]; ok {
		return true
	}
	if !s.allowAll {
		return false
	}
	return IsBuiltInToolName(normalized)
}

func toolInputSchema(parameters any) map[string]any {
	if parameters == nil {
		return emptyObjectSchema()
	}
	if schema, ok := parameters.(map[string]any); ok && schema != nil {
		return schema
	}
	raw, err := json.Marshal(parameters)
	if err != nil {
		return emptyObjectSchema()
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil || schema == nil {
		return emptyObjectSchema()
	}
	if strings.TrimSpace(StringArg(schema, "type")) == "" {
		schema["type"] = "object"
	}
	return schema
}
