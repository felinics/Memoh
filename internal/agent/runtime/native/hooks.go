package native

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type hookToolRunner struct {
	tools map[string]sdk.Tool
}

type hookForceApprovalKey struct{}

func ContextWithHookForcedApproval(ctx context.Context, reason string) context.Context {
	return context.WithValue(ctx, hookForceApprovalKey{}, strings.TrimSpace(reason))
}

func HookForcedApprovalReason(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	reason, ok := ctx.Value(hookForceApprovalKey{}).(string)
	return strings.TrimSpace(reason), ok
}

func (r hookToolRunner) RunHookTool(ctx context.Context, toolName string, input map[string]any) (any, error) {
	tool, ok := r.tools[strings.TrimSpace(toolName)]
	if !ok || tool.Execute == nil {
		return nil, fmt.Errorf("hook tool %q not found", toolName)
	}
	return tool.Execute(&sdk.ToolExecContext{
		Context:    ctx,
		ToolName:   tool.Name,
		ToolCallID: "hook:" + tool.Name,
	}, input)
}

func (a *Agent) wrapToolsWithHooks(ctx context.Context, cfg RunConfig, sdkTools []sdk.Tool) []sdk.Tool {
	if a == nil || a.hookService == nil || len(sdkTools) == 0 {
		return sdkTools
	}
	originalByName := make(map[string]sdk.Tool, len(sdkTools))
	for _, tool := range sdkTools {
		originalByName[tool.Name] = tool
	}
	runner := hookToolRunner{tools: originalByName}
	wrapped := make([]sdk.Tool, len(sdkTools))
	for i, tool := range sdkTools {
		originalExecute := tool.Execute
		toolName := tool.Name
		wrapped[i] = tool
		if originalExecute == nil {
			continue
		}
		wrapped[i].Execute = func(execCtx *sdk.ToolExecContext, input any) (any, error) {
			output, execErr := originalExecute(execCtx, input)
			if execErr != nil {
				errReq := a.baseHookRequest(ctx, cfg, hooks.EventToolError)
				errReq.Tool = &hooks.ToolPayload{
					Name:   toolName,
					CallID: toolCallID(execCtx),
					Input:  input,
					Error:  execErr.Error(),
				}
				errReq.Error = execErr.Error()
				if _, hookErr := a.hookService.Run(execContext(ctx, execCtx), errReq, runner); hookErr != nil {
					return output, fmt.Errorf("tool error hook failed after tool error: %w: %w", execErr, hookErr)
				}
				return output, execErr
			}
			postReq := a.baseHookRequest(ctx, cfg, hooks.EventPostToolUse)
			postReq.Tool = &hooks.ToolPayload{
				Name:   toolName,
				CallID: toolCallID(execCtx),
				Input:  input,
				Result: output,
			}
			if _, hookErr := a.hookService.Run(execContext(ctx, execCtx), postReq, runner); hookErr != nil {
				return output, fmt.Errorf("post tool hook failed for %q: %w", toolName, hookErr)
			}
			return output, nil
		}
	}
	return wrapped
}

func (a *Agent) wrapApprovalHandlerWithHooks(cfg RunConfig, sdkTools []sdk.Tool, next func(context.Context, sdk.ToolCall) (sdk.ToolApprovalResult, error)) func(context.Context, sdk.ToolCall) (sdk.ToolApprovalResult, error) {
	if a == nil || a.hookService == nil {
		return next
	}
	originalByName := make(map[string]sdk.Tool, len(sdkTools))
	for _, tool := range sdkTools {
		originalByName[tool.Name] = tool
	}
	runner := hookToolRunner{tools: originalByName}
	return func(ctx context.Context, call sdk.ToolCall) (sdk.ToolApprovalResult, error) {
		limitLabel := "tool result (" + call.ToolName + ")"
		limitText := func(text string) string {
			return contextlimit.LimitString(text, limitLabel, a.Limits().ToolOutputLimit())
		}
		limitErr := func(err error) error {
			return contextlimit.LimitError(err, limitLabel, a.Limits().ToolOutputLimit())
		}
		limitResult := func(result sdk.ToolApprovalResult) sdk.ToolApprovalResult {
			if result.Decision == sdk.ToolApprovalDecisionRejected {
				result.Reason = limitText(result.Reason)
			}
			return result
		}
		req := a.baseHookRequest(ctx, cfg, hooks.EventPreToolUse)
		req.Tool = &hooks.ToolPayload{
			Name:   call.ToolName,
			CallID: call.ToolCallID,
			Input:  call.Input,
		}
		res, err := a.hookService.Run(ctx, req, runner)
		if err != nil {
			if errors.Is(err, hooks.ErrDenied) || res.Decision == hooks.DecisionDeny {
				return sdk.ToolApprovalResult{
					Decision: sdk.ToolApprovalDecisionRejected,
					Reason:   limitText(firstHookText(res.Reason, err.Error())),
				}, nil
			}
			return sdk.ToolApprovalResult{}, limitErr(fmt.Errorf("pre tool hook failed for %q: %w", call.ToolName, err))
		}
		switch res.Decision {
		case hooks.DecisionDeny:
			return sdk.ToolApprovalResult{
				Decision: sdk.ToolApprovalDecisionRejected,
				Reason:   limitText(firstHookText(res.Reason, "denied by hook")),
			}, nil
		case hooks.DecisionAskApproval:
			if next == nil {
				return sdk.ToolApprovalResult{
					Decision: sdk.ToolApprovalDecisionRejected,
					Reason:   limitText(firstHookText(res.Reason, "hook requested approval but no approval service is configured")),
				}, nil
			}
			result, err := next(ContextWithHookForcedApproval(ctx, limitText(res.Reason)), call)
			return limitResult(result), err
		}
		if next == nil {
			return sdk.ToolApprovalResult{Decision: sdk.ToolApprovalDecisionApproved}, nil
		}
		result, err := next(ctx, call)
		return limitResult(result), err
	}
}

func (a *Agent) baseHookRequest(ctx context.Context, cfg RunConfig, event string) hooks.Request {
	return hooks.Request{
		Version:   1,
		Event:     event,
		BotID:     cfg.Identity.BotID,
		SessionID: cfg.Identity.SessionID,
		ChatID:    cfg.Identity.ChatID,
		Workspace: a.hookWorkspace(ctx, cfg.Identity.BotID),
	}
}

func (a *Agent) runTurnHook(ctx context.Context, cfg RunConfig, eventName, errMsg string) {
	if a == nil || a.hookService == nil {
		return
	}
	req := a.baseHookRequest(ctx, cfg, eventName)
	req.Turn = map[string]any{
		"event":        eventName,
		"session_type": cfg.SessionType,
		"model":        modelID(cfg.Model),
	}
	if strings.TrimSpace(errMsg) != "" {
		req.Error = strings.TrimSpace(errMsg)
		req.Turn["error"] = req.Error
	}
	if _, err := a.hookService.Run(ctx, req, nil); err != nil && a.logger != nil {
		a.logger.Warn("turn hook failed",
			slog.String("event", eventName),
			slog.String("bot_id", cfg.Identity.BotID),
			slog.String("session_id", cfg.Identity.SessionID),
			slog.Any("error", err),
		)
	}
}

func (a *Agent) applyBeforeModelCallHook(ctx context.Context, cfg RunConfig, step int) (RunConfig, error) {
	if a == nil || a.hookService == nil {
		return cfg, nil
	}
	req := a.baseHookRequest(ctx, cfg, hooks.EventBeforeModelCall)
	req.Turn = modelCallHookPayload(cfg, step, 0)
	res, err := a.hookService.Run(ctx, req, nil)
	if err != nil {
		if errors.Is(err, hooks.ErrDenied) || res.Decision == hooks.DecisionDeny {
			return cfg, fmt.Errorf("%w: %s", hooks.ErrDenied, firstHookText(res.Reason, err.Error()))
		}
		return cfg, fmt.Errorf("before model call hook failed: %w", err)
	}
	if strings.TrimSpace(res.AppendContext) != "" {
		cfg = applyBeforeModelCallAppendContext(cfg, res.AppendContext)
		if a.contextViewApplier == nil {
			cfg = cfg.RefreshContextFrag()
		}
	}
	return cfg, nil
}

func applyBeforeModelCallAppendContext(cfg RunConfig, appendContext string) RunConfig {
	if strings.TrimSpace(appendContext) == "" {
		return cfg
	}
	cfg.Messages = append(cfg.Messages, sdk.UserMessage(formatHookContext(hooks.EventBeforeModelCall, appendContext)))
	cfg.ContextMutations.Record(contextfrag.MutationBeforeModelCallHook, fmt.Sprintf("append_bytes=%d", len(appendContext)))
	return cfg
}

func (a *Agent) wrapPrepareStepWithModelHook(ctx context.Context, cfg RunConfig, base func(*sdk.GenerateParams) *sdk.GenerateParams) func(*sdk.GenerateParams) *sdk.GenerateParams {
	if a == nil || a.hookService == nil {
		return base
	}
	step := 1
	return func(p *sdk.GenerateParams) *sdk.GenerateParams {
		if base != nil {
			if override := base(p); override != nil {
				p = override
			}
		}
		currentStep := step
		req := a.baseHookRequest(ctx, cfg, hooks.EventBeforeModelCall)
		req.Turn = modelCallHookPayload(cfg, currentStep, len(p.Messages))
		step++
		res, err := a.hookService.Run(ctx, req, nil)
		if err != nil {
			if a.logger != nil {
				a.logger.Warn("before model call hook failed",
					slog.String("bot_id", cfg.Identity.BotID),
					slog.String("session_id", cfg.Identity.SessionID),
					slog.Any("error", err),
				)
			}
			return p
		}
		return applyStepHookAppendContext(p, cfg.ContextMutations, currentStep, res.AppendContext)
	}
}

func applyStepHookAppendContext(p *sdk.GenerateParams, ledger *contextfrag.MutationLedger, step int, appendContext string) *sdk.GenerateParams {
	if strings.TrimSpace(appendContext) == "" {
		return p
	}
	p.Messages = append(p.Messages, sdk.UserMessage(formatHookContext(hooks.EventBeforeModelCall, appendContext)))
	ledger.Record(contextfrag.MutationBeforeModelCallHook, fmt.Sprintf("step=%d append_bytes=%d", step, len(appendContext)))
	return p
}

func (a *Agent) runAfterModelCallHook(ctx context.Context, cfg RunConfig, step *sdk.StepResult, stepIndex int) {
	if a == nil || a.hookService == nil || step == nil {
		return
	}
	req := a.baseHookRequest(ctx, cfg, hooks.EventAfterModelCall)
	payload := modelCallHookPayload(cfg, stepIndex, len(step.Messages))
	payload["finish_reason"] = string(step.FinishReason)
	payload["raw_finish_reason"] = step.RawFinishReason
	payload["input_tokens"] = step.Usage.InputTokens
	payload["output_tokens"] = step.Usage.OutputTokens
	payload["total_tokens"] = step.Usage.TotalTokens
	payload["tool_call_count"] = len(step.ToolCalls)
	payload["tool_result_count"] = len(step.ToolResults)
	if step.DeferredToolApproval != nil {
		payload["deferred_approval_id"] = step.DeferredToolApproval.ApprovalID
	}
	req.Turn = payload
	if _, err := a.hookService.Run(context.WithoutCancel(ctx), req, nil); err != nil && a.logger != nil {
		a.logger.Warn("after model call hook failed",
			slog.String("bot_id", cfg.Identity.BotID),
			slog.String("session_id", cfg.Identity.SessionID),
			slog.Any("error", err),
		)
	}
}

func (a *Agent) hookWorkspace(ctx context.Context, botID string) hooks.WorkspaceInfo {
	info := hooks.WorkspaceInfo{
		CWD:     hooks.DefaultWorkDir,
		Runtime: bridge.WorkspaceBackendContainer,
	}
	if a == nil || a.bridgeProvider == nil {
		return info
	}
	provider, ok := a.bridgeProvider.(bridge.WorkspaceInfoProvider)
	if !ok {
		return info
	}
	workspaceInfo, err := provider.WorkspaceInfo(ctx, botID)
	if err != nil {
		return info
	}
	if strings.TrimSpace(workspaceInfo.DefaultWorkDir) != "" {
		info.CWD = workspaceInfo.DefaultWorkDir
	}
	if strings.TrimSpace(workspaceInfo.Backend) != "" {
		info.Runtime = workspaceInfo.Backend
	}
	return info
}

func execContext(fallback context.Context, execCtx *sdk.ToolExecContext) context.Context {
	if execCtx != nil && execCtx.Context != nil {
		return execCtx.Context
	}
	return fallback
}

func toolCallID(execCtx *sdk.ToolExecContext) string {
	if execCtx == nil {
		return ""
	}
	return execCtx.ToolCallID
}

func firstHookText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func modelID(model *sdk.Model) string {
	if model == nil {
		return ""
	}
	return strings.TrimSpace(model.ID)
}

func modelCallHookPayload(cfg RunConfig, step int, messageCount int) map[string]any {
	return map[string]any{
		"session_type":  cfg.SessionType,
		"model":         modelID(cfg.Model),
		"step":          step,
		"message_count": messageCount,
	}
}

func formatHookContext(eventName, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return "[Hook Context: " + strings.TrimSpace(eventName) + "]\n" + text
}
