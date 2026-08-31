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
// --mcp-config JSON naming it. An empty string means no gateway this turn;
// the mount (when non-nil) must be stopped when the turn ends.
func (d *Driver) mountTurnTools(ctx context.Context, client *bridge.Client, workspaceInfo bridge.WorkspaceInfo, input external.PromptInput) (string, *toolmount.Mount, error) {
	baseURL := toolmount.ResolveBaseURL(workspaceInfo, input.ToolHTTPURL)
	if baseURL == "" {
		return "", nil, fmt.Errorf("resolve Memoh tool gateway URL for %s workspace", workspaceInfo.Backend)
	}
	session := turnToolSession(ctx, input)
	mount, err := toolmount.Serve(ctx, client, baseURL, d.toolGateway, func() mcp.ToolSessionContext {
		return session
	})
	if err != nil {
		return "", nil, fmt.Errorf("mount Memoh tool gateway: %w", err)
	}
	config := fmt.Sprintf(
		`{"mcpServers":{"%s":{"type":"http","url":%s,"timeout":%d}}}`,
		memohMCPServerName,
		strconv.Quote(mount.URL),
		MemohToolTimeoutMillis,
	)
	return config, mount, nil
}

// turnToolSession builds the trusted tool identity for one turn. Everything
// comes from the prompt input and the turn context, never from HTTP requests.
func turnToolSession(ctx context.Context, input external.PromptInput) mcp.ToolSessionContext {
	session := mcp.ToolSessionContext{
		BotID:                     input.BotID,
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
