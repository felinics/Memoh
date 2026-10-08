package native

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/models"
)

// maxCapabilityRefreshes bounds how often one turn may re-assemble its tools:
// a tool that reports a change on every call must not keep the loop alive.
const maxCapabilityRefreshes = 32

// wrapExecutableTools builds the execution chain over assembled tools: the
// UI-only payload stripper innermost (so the payload is recorded whole and
// never counts against the model's output budget), output limits, the
// bot-defined hooks, limits again for hook output, and the loop guard. The
// approval set is the pre-hook view: the approval handler must see the tool
// the hooks will run, not the hook wrapper.
func (a *Agent) wrapExecutableTools(
	hookCtx context.Context,
	cfg RunConfig,
	sdkTools []toolexec.Tool,
	meta *toolExecutionMetadataRegistry,
	guard *ToolLoopGuard,
	abortCallIDs *toolAbortRegistry,
) (exec, approval []toolexec.Tool) {
	limit := a.Limits().ToolOutputLimit()
	sdkTools = meta.wrapToolUIOutput(sdkTools)
	sdkTools = tools.WrapToolOutputLimits(sdkTools, limit)
	approval = append([]toolexec.Tool(nil), sdkTools...)
	sdkTools = a.wrapToolsWithHooks(hookCtx, cfg, sdkTools)
	sdkTools = tools.WrapToolOutputLimits(sdkTools, limit)
	if guard != nil {
		sdkTools = wrapToolsWithLoopGuard(sdkTools, guard, abortCallIDs)
	}
	return sdkTools, approval
}

// refreshedTools is a re-assembled tool set ready for the loop's next model
// call: the executable chain, the provider-facing definitions, the approval
// handler over the new approval set, and the system prompt carrying the new
// tool usage.
type refreshedTools struct {
	exec    []toolexec.Tool
	defs    []sdk.ToolDefinition
	approve func(context.Context, sdk.ToolCall) (toolexec.ToolApprovalResult, error)
	system  string
	// wrapped and approval are the inputs buildGenerateDispatch takes; the
	// stream engine keeps them so a mid-stream retry after the refresh
	// rebuilds its dispatch from the refreshed set, not the segment's first.
	wrapped  []toolexec.Tool
	approval []toolexec.Tool
}

// refreshCapabilities re-assembles the tool set after a tool reported that
// the bot's capabilities changed (an MCP connection or an App was installed).
// The loop snapshots executable tools when a dispatch is built, so without
// this the model would learn about a new tool only on the next turn, and a
// definition-only refresh would leave the old handlers behind.
//
// The refresh is in place: the thread, the durable step cursor and the
// provider-attempt state continue unchanged; only the tools, their usage text
// in the system prompt, and the definitions on the wire are replaced.
func (a *Agent) refreshCapabilities(
	ctx, hookCtx context.Context,
	cfg *RunConfig,
	emitter tools.StreamEmitter,
	liveToolStream bool,
	readMedia *readMediaDecorationState,
	meta *toolExecutionMetadataRegistry,
	guard *ToolLoopGuard,
	abortCallIDs *toolAbortRegistry,
) (refreshedTools, error) {
	if cfg.capabilityRefreshCount >= maxCapabilityRefreshes {
		return refreshedTools{}, errors.New("too many capability refreshes in one turn")
	}
	cfg.capabilityRefreshCount++
	sdkTools, usage, usageFrags, defs, err := a.assembleTools(ctx, *cfg, emitter, liveToolStream)
	if err != nil {
		return refreshedTools{}, fmt.Errorf("assemble tools: %w", err)
	}
	if cfg.ContextToolUsage != "" {
		cfg.System = strings.Replace(cfg.System, cfg.ContextToolUsage, "", 1)
	}
	cfg.ContextToolUsage = ""
	cfg.ContextToolUsageFrags = nil
	cfg.ContextToolDefs = defs
	if usage != "" {
		cfg.System = appendToolUsageToSystem(cfg.System, usage)
		cfg.ContextToolUsage = usage
		cfg.ContextToolUsageFrags = usageFrags
	}
	sdkTools = decorateReadMediaToolsWithState(cfg.Model, sdkTools, readMedia)
	exec, approval := a.wrapExecutableTools(hookCtx, *cfg, sdkTools, meta, guard, abortCallIDs)
	wrapped := exec
	exec = canonicalizeProviderToolSchemas(exec)
	// The definitions carry the same prompt-cache marking the dispatch put on
	// the original set, so the refreshed request stays cacheable.
	_, _, planTools, _, _ := models.ApplyPromptCacheWithPlan(cfg.Model, cfg.PromptCacheTTL, cfg.ContextCachePlan, cfg.System, cfg.Messages, exec)
	var executable []toolexec.Tool
	if len(planTools) > 0 && cfg.SupportsToolCall {
		executable = planTools
	}
	toolDefs, err := toolexec.ToolDefinitionsFromTools(executable)
	if err != nil {
		return refreshedTools{}, err
	}
	approve := cfg.ToolApprovalHandler
	if a != nil && a.hookService != nil {
		approve = a.wrapApprovalHandlerWithHooks(*cfg, approval, approve)
	}
	return refreshedTools{exec: executable, defs: toolDefs, approve: approve, system: cfg.System, wrapped: wrapped, approval: approval}, nil
}

// apply installs a refreshed tool set on the dispatch and the request the
// loop will send next. The system prompt travels either as the request's
// System field or, when the prompt-cache plan promoted it, as the first
// message of the prefix; the refreshed usage text replaces it in whichever
// place it lives, so the model that sees the new tools also sees their
// instructions. The returned messages are the conversation with the promoted
// prefix rewritten (unchanged when nothing was promoted).
func (r refreshedTools) apply(dispatch *generateDispatch, params *sdk.Request, messages []sdk.Message) []sdk.Message {
	dispatch.execTools = r.exec
	dispatch.approve = r.approve
	params.Tools = r.defs
	if !dispatch.systemPrepended {
		params.System = r.system
		return messages
	}
	params.Messages = replacePromotedSystem(params.Messages, r.system)
	return replacePromotedSystem(messages, r.system)
}

// replacePromotedSystem rewrites the text of the system message the
// prompt-cache plan placed at the head of the prefix, keeping its cache
// control. Messages without such a head are returned as they are.
func replacePromotedSystem(messages []sdk.Message, system string) []sdk.Message {
	// An empty refreshed prompt keeps the head as it is: a system message
	// with an empty text part is a request some providers reject.
	if system == "" || len(messages) == 0 || messages[0].Role != sdk.MessageRoleSystem || len(messages[0].Content) == 0 {
		return messages
	}
	text, ok := messages[0].Content[0].(sdk.TextPart)
	if !ok || text.Text == system {
		return messages
	}
	text.Text = system
	content := append([]sdk.MessagePart(nil), messages[0].Content...)
	content[0] = text
	head := messages[0]
	head.Content = content
	out := make([]sdk.Message, len(messages))
	copy(out, messages)
	out[0] = head
	return out
}
