// Memoh tool-gateway injection for codex threads. Each thread gets its own
// reverse-HTTP route into the workspace tools proxy, configured through the
// thread's `mcp_servers` config override at start/resume/fork. The route outlives
// individual turns (thread config is start-time), so the tool session
// identity resolves live from the thread's active turn.
package codex

import (
	"context"
	"log/slog"
	"sync"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/toolmount"
	"github.com/felinics/memoh/internal/mcp"
)

// memohMCPServerName is the config key codex shows in tool names
// (memoh__send_message and friends).
const memohMCPServerName = "memoh"

// MemohToolTimeoutSec caps one Memoh gateway tool call on the codex side.
// Memoh tools can legitimately block on a human decision (approval cards,
// ask_user) for up to their wait windows; the toolmount timeout-ladder guard
// test pins this above them.
const MemohToolTimeoutSec = 900

// threadRef carries the thread id into the mount's session resolver; the id
// is only known after thread/start returns.
type threadRef struct {
	mu sync.Mutex
	id string
}

func (r *threadRef) set(id string) {
	r.mu.Lock()
	r.id = id
	r.mu.Unlock()
}

func (r *threadRef) get() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.id
}

// prepareThreadTools mounts the gateway for a thread about to start,
// resume, or fork. The returned function binds the mount to the thread or
// closes it when creation fails. A gateway that cannot be mounted gives no
// config: the thread runs without Memoh tools, and every turn says so.
func (d *Driver) prepareThreadTools(ctx context.Context, srv *appServer, input external.PromptInput) (map[string]any, func(threadID string)) {
	ref := &threadRef{}
	//nolint:contextcheck // the route serves every later turn of the thread, not only the one that opened it.
	mount, err := toolmount.Serve(srv.mountCtx, srv.client, srv.workspaceInfo, input.ToolHTTPURL, d.toolGateway, func() mcp.ToolSessionContext {
		return srv.toolSessionForThread(ref.get())
	})
	if err != nil {
		d.logger.WarnContext(ctx, "codex: Memoh tool gateway unavailable", slog.String("bot_id", srv.botID), slog.Any("error", err))
		return nil, func(threadID string) { srv.setThreadToolless(threadID, threadID != "") }
	}
	config := map[string]any{
		"features.default_mode_request_user_input": true,
		"mcp_servers": map[string]any{
			memohMCPServerName: map[string]any{
				"url":              mount.URL,
				"tool_timeout_sec": MemohToolTimeoutSec,
			},
		},
	}
	return config, func(threadID string) {
		if threadID == "" {
			mount.Stop()
			return
		}
		ref.set(threadID)
		srv.setThreadToolless(threadID, false)
		srv.registerToolMount(threadID, mount)
	}
}

// registerToolMount records a thread's live gateway mount, replacing (and
// stopping) any previous one for the same thread.
func (s *appServer) registerToolMount(threadID string, mount *toolmount.Mount) {
	s.mu.Lock()
	previous := s.toolMounts[threadID]
	s.toolMounts[threadID] = mount
	s.mu.Unlock()
	if previous != nil {
		previous.Stop()
	}
}

// stopToolMounts tears down every thread mount; used on server close.
func (s *appServer) stopToolMounts() {
	s.mu.Lock()
	mounts := make([]*toolmount.Mount, 0, len(s.toolMounts))
	for _, mount := range s.toolMounts {
		mounts = append(mounts, mount)
	}
	s.toolMounts = map[string]*toolmount.Mount{}
	s.mu.Unlock()
	for _, mount := range mounts {
		mount.Stop()
	}
}

// toolSessionForThread resolves the trusted tool identity for a thread's MCP
// requests: the turn running on the thread, or the bot alone between turns.
func (s *appServer) toolSessionForThread(threadID string) mcp.ToolSessionContext {
	turn := s.turnForThread(threadID)
	if threadID == "" || turn == nil {
		return toolmount.IdleSession(s.botID)
	}
	session := toolmount.TurnSession(turn.ctx, turn.input)
	session.SupportsImageInput = true
	return session
}
