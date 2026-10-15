// Memoh tool-gateway injection for Claude Code turns. Each turn's CLI
// process gets its own reverse-HTTP route into the workspace tools proxy via
// --mcp-config; the route lives exactly as long as the turn.
package claudecode

import (
	"context"
	"fmt"
	"strconv"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/toolmount"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// memohMCPServerName is the config key claude shows in tool names
// (mcp__memoh__send_message and friends).
const memohMCPServerName = "memoh"

// MemohToolTimeoutMillis caps one Memoh gateway tool call on the claude side
// (milliseconds, per the pinned SDK contract). Memoh tools can legitimately
// block on a human decision (approval cards, ask_user) for up to their wait
// windows; the toolmount timeout-ladder guard test pins this above them.
const MemohToolTimeoutMillis = 900_000

// mountTurnTools serves the gateway for one turn and returns the
// --mcp-config JSON naming it and the stop that ends the turn's tools. Tool
// calls belong to the run: they end when ctx does or when stop is called. The
// route outlives ctx until stop, so calls cut short during the CLI's
// interrupt handshake still deliver their answers.
func (d *Driver) mountTurnTools(ctx context.Context, client *bridge.Client, workspaceInfo bridge.WorkspaceInfo, input external.PromptInput) (string, func(), error) {
	baseURL := toolmount.ResolveBaseURL(workspaceInfo, input.ToolHTTPURL)
	if baseURL == "" {
		return "", nil, errs.New(fmt.Sprintf("resolve Memoh tool gateway URL for %s workspace", workspaceInfo.Backend))
	}
	toolsCtx, cancelTools := context.WithCancel(ctx)
	session := turnToolSession(toolsCtx, input)
	mount, err := toolmount.Serve(context.WithoutCancel(ctx), client, baseURL, d.toolGateway, func() mcp.ToolSessionContext {
		return session
	})
	if err != nil {
		cancelTools()
		return "", nil, fmt.Errorf("mount Memoh tool gateway: %w", err)
	}
	config := fmt.Sprintf(
		`{"mcpServers":{"%s":{"type":"http","url":%s,"timeout":%d}}}`,
		memohMCPServerName,
		strconv.Quote(mount.URL),
		MemohToolTimeoutMillis,
	)
	return config, func() {
		cancelTools()
		mount.Stop()
	}, nil
}

// turnToolSession builds the trusted tool identity for one turn. Everything
// comes from the prompt input and the turn context, never from HTTP requests.
func turnToolSession(ctx context.Context, input external.PromptInput) mcp.ToolSessionContext {
	session := mcp.ToolSessionContext{
		BotID:                     input.BotID,
		UserID:                    input.ActorUserID,
		ChatID:                    firstNonEmpty(input.ChatID, input.BotID),
		SessionID:                 input.ThreadID,
		RunID:                     input.RunID,
		SessionType:               firstNonEmpty(input.SessionMode, sessionmode.Chat),
		RouteID:                   input.RouteID,
		CurrentPlatform:           input.CurrentPlatform,
		ReplyTarget:               input.ReplyTarget,
		ConversationType:          input.ConversationType,
		ChannelIdentityID:         input.ChannelIdentityID,
		SessionToken:              input.SessionToken,
		CanRequestUserInput:       input.CanRequestUserInput,
		IsSubagent:                input.IsSubagent(),
		CanListUserInput:          true,
		RuntimeActive:             true,
		RequireActiveRun:          true,
		SupportsImageInput:        true,
		ContextBudgetMaxTokens:    input.ContextBudgetMaxTokens,
		ContextToolExchangePolicy: input.ContextToolExchangePolicy,
	}
	if fence, ok := runtimefence.FromContext(ctx); ok {
		session.RuntimeFence = fence
	}
	session.RunContext = ctx
	return session
}
