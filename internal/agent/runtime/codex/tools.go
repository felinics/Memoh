// Memoh tool-gateway injection for codex threads. Each thread gets its own
// reverse-HTTP route into the workspace tools proxy, configured through the
// thread's `mcp_servers` config override at start/resume. The route outlives
// individual turns (thread config is start-time), so the tool session
// identity resolves live from the thread's active turn.
package codex

import (
	"fmt"
	"strings"
	"sync"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/toolmount"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/runtimefence"
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

// prepareThreadTools mounts the gateway for a thread about to start or
// resume. The returned function binds the mount to the created thread or
// closes it when creation fails.
func (d *Driver) prepareThreadTools(srv *appServer, input external.PromptInput) (map[string]any, func(threadID string), error) {
	baseURL := toolmount.ResolveBaseURL(srv.workspaceInfo, input.ToolHTTPURL)
	if baseURL == "" {
		return nil, nil, fmt.Errorf("resolve Memoh tool gateway URL for %s workspace", srv.workspaceInfo.Backend)
	}
	ref := &threadRef{}
	mount, err := toolmount.Serve(srv.mountCtx, srv.client, baseURL, d.toolGateway, func() mcp.ToolSessionContext {
		return srv.toolSessionForThread(ref.get())
	})
	if err != nil {
		return nil, nil, fmt.Errorf("mount Memoh tool gateway: %w", err)
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
		srv.registerToolMount(threadID, mount)
	}, nil
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
// requests: bot identity always, plus the live per-turn fields while a turn
// runs. It never trusts anything from the HTTP request. The mount outlives
// turns, so RequireActiveRun keeps the idle window to tools/list — a call
// with no live turn has no run to own it and is refused at the gateway.
func (s *appServer) toolSessionForThread(threadID string) mcp.ToolSessionContext {
	session := mcp.ToolSessionContext{
		BotID:            s.botID,
		ChatID:           s.botID,
		SessionType:      sessionmode.Chat,
		CanListUserInput: true,
		RequireActiveRun: true,
	}
	if threadID == "" {
		return session
	}
	turn := s.turnForThread(threadID)
	if turn == nil {
		return session
	}
	in := turn.input
	overlay := func(dst *string, value string) {
		if value = strings.TrimSpace(value); value != "" {
			*dst = value
		}
	}
	overlay(&session.ChatID, in.ChatID)
	overlay(&session.SessionID, in.ThreadID)
	overlay(&session.RunID, in.RunID)
	overlay(&session.SessionType, in.SessionMode)
	overlay(&session.RouteID, in.RouteID)
	overlay(&session.CurrentPlatform, in.CurrentPlatform)
	overlay(&session.ReplyTarget, in.ReplyTarget)
	overlay(&session.ConversationType, in.ConversationType)
	overlay(&session.ChannelIdentityID, in.ChannelIdentityID)
	overlay(&session.SessionToken, in.SessionToken)
	session.CanRequestUserInput = in.CanRequestUserInput
	session.RuntimeActive = true
	session.SupportsImageInput = true
	session.ContextBudgetMaxTokens = in.ContextBudgetMaxTokens
	session.ContextToolExchangePolicy = in.ContextToolExchangePolicy
	if fence, ok := runtimefence.FromContext(turn.ctx); ok {
		session.RuntimeFence = fence
	}
	session.RunContext = turn.ctx
	return session
}
