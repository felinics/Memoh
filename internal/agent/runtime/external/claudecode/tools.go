// Memoh tool-gateway injection for Claude Code turns. Each turn's CLI
// process gets its own reverse-HTTP route into the workspace tools proxy via
// --mcp-config; the route lives exactly as long as the turn.
package claudecode

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/toolmount"
	"github.com/felinics/memoh/internal/mcp"
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
// --mcp-config JSON naming it. A gateway that cannot be mounted leaves the
// turn without Memoh tools, said in the conversation; the mount (when
// non-nil) must be stopped when the turn ends.
func (d *Driver) mountTurnTools(ctx context.Context, client *bridge.Client, workspaceInfo bridge.WorkspaceInfo, input external.PromptInput) (string, *toolmount.Mount) {
	session := toolmount.TurnSession(ctx, input)
	session.SupportsImageInput = true
	mount, err := toolmount.Serve(ctx, client, workspaceInfo, input.ToolHTTPURL, d.toolGateway, func() mcp.ToolSessionContext {
		return session
	})
	if err != nil {
		d.logger.WarnContext(ctx, "claude: Memoh tool gateway unavailable", slog.String("bot_id", input.BotID), slog.Any("error", err))
		toolmount.EmitUnavailableNotice(input.Sink, "the tool gateway could not be mounted")
		return "", nil
	}
	config := fmt.Sprintf(
		`{"mcpServers":{"%s":{"type":"http","url":%s,"timeout":%d}}}`,
		memohMCPServerName,
		strconv.Quote(mount.URL),
		MemohToolTimeoutMillis,
	)
	return config, mount
}
