// Memoh tool-gateway injection for Claude Code. Each CLI process gets its own
// reverse-HTTP route into the workspace tools proxy via --mcp-config; the
// route lives exactly as long as the process.
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

// mountSessionTools serves the gateway for a CLI process's life and returns
// the --mcp-config JSON naming it. Tool calls take the identity of the turn
// running when they arrive; between turns the gateway refuses to run them. A
// gateway that cannot be mounted leaves the process without Memoh tools, and
// every turn on it says so.
func (d *Driver) mountSessionTools(ctx context.Context, client *bridge.Client, workspaceInfo bridge.WorkspaceInfo, input external.PromptInput, current func() *turnRunner) (string, *toolmount.Mount) {
	//nolint:contextcheck // the route serves every later turn of the process, not only the one that started it.
	mount, err := toolmount.Serve(context.WithoutCancel(ctx), client, workspaceInfo, input.ToolHTTPURL, d.toolGateway, func() mcp.ToolSessionContext {
		t := current()
		if t == nil {
			return toolmount.IdleSession(input.BotID)
		}
		session := toolmount.TurnSession(t.ctx, t.input)
		session.SupportsImageInput = true
		return session
	})
	if err != nil {
		d.logger.WarnContext(ctx, "claude: Memoh tool gateway unavailable", slog.String("bot_id", input.BotID), slog.Any("error", err))
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
