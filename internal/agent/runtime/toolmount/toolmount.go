// Package toolmount serves the Memoh tool gateway (HTTP MCP) into a bot
// workspace over the bridge's reverse-HTTP stream, so an external agent runtime
// inside the container reaches Memoh tools at the workspace-local proxy URL.
// Each mount owns one unguessable route; the tool session identity resolves
// live per request, so one mount can outlive many turns.
package toolmount

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// Gateway is the Memoh MCP tool surface a mount serves.
type Gateway struct {
	Tools    *mcp.ToolGatewayService
	Contexts *mcp.ToolSessionContextStore
	Logger   *slog.Logger
}

// ResolveBaseURL picks the mount base for a workspace. Container-backed
// workspaces always use the workspace-local tools proxy — the only address
// the agent inside the container can reach; the request-derived URL is
// meaningless there (and empty behind proxies that rewrite Host). Other
// backends fall back to the caller-provided externally reachable URL.
func ResolveBaseURL(info bridge.WorkspaceInfo, inputURL string) string {
	backend := strings.TrimSpace(info.Backend)
	if backend == "" || backend == bridge.WorkspaceBackendContainer {
		return strings.TrimSpace(info.ACPToolsHTTPURL)
	}
	return strings.TrimSpace(inputURL)
}

// Mount is one live reverse-HTTP tool route inside a workspace.
type Mount struct {
	// URL is the workspace-local MCP endpoint for the runtime's config.
	URL string

	stopMu sync.Mutex
	stop   func()
}

// Stop tears the route down. Safe to call more than once, from any
// goroutine: thread-mount replacement and app-server shutdown may both hold
// the same Mount.
func (m *Mount) Stop() {
	m.stopMu.Lock()
	stop := m.stop
	m.stop = nil
	m.stopMu.Unlock()
	if stop != nil {
		stop()
	}
}

// Serve mounts the gateway behind a freshly minted route under baseURL (the
// workspace-local tools proxy address). session resolves the trusted tool
// identity per request; it must never trust anything from the HTTP request.
// The mount lives until Stop or until ctx ends.
func Serve(ctx context.Context, client *bridge.Client, baseURL string, gateway Gateway, session func() mcp.ToolSessionContext) (*Mount, error) {
	guardedURL, guardedPath, err := mintRouteURL(baseURL)
	if err != nil {
		return nil, err
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req == nil || req.URL == nil || req.URL.Path != guardedPath {
			http.NotFound(w, req)
			return
		}
		mcp.ServeToolMCPHTTP(w, req, gateway.Logger, gateway.Tools, gateway.Contexts, session())
	})
	stop, err := client.ServeReverseHTTPRoute(ctx, guardedPath, handler)
	if err != nil {
		return nil, fmt.Errorf("serve tool gateway route: %w", err)
	}
	return &Mount{URL: guardedURL, stop: stop}, nil
}

// mintRouteURL appends an unguessable path segment to the workspace-local
// tools proxy URL; the segment doubles as the reverse-HTTP route key.
func mintRouteURL(rawURL string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("invalid Memoh tools URL %q", rawURL)
	}
	basePath := strings.TrimRight(u.Path, "/")
	if basePath == "" {
		basePath = "/mcp"
	}
	u.Path = basePath + "/" + uuid.NewString()
	return u.String(), u.Path, nil
}

// EmitUnavailableNotice makes a tool-plane degradation visible in the
// conversation instead of only in server logs.
func EmitUnavailableNotice(sink external.EventSink, reason string) {
	sink.EmitStreamEvent(event.StreamEvent{
		Type:  event.RuntimeNotice,
		Code:  "tools_unavailable",
		Delta: "Memoh tools are unavailable for this turn: " + reason,
	})
}

// RegisterTurnSink routes gateway tool events (tool cards, approval requests,
// ask_user prompts from Memoh MCP tools) for one turn into its stream and
// returns the unregister func. Without this registration the approval flow
// sees every request as undeliverable and rejects it.
func RegisterTurnSink(contexts *mcp.ToolSessionContextStore, botID, sessionID, runID string, emit func(event.StreamEvent)) func() {
	return contexts.RegisterToolEventSink(mcp.ToolSessionContext{
		BotID:     botID,
		SessionID: sessionID,
		RunID:     runID,
	}, func(toolEvent mcp.ToolStreamEvent) {
		if streamEvent, ok := toolEvent.ToAgentStreamEvent(); ok {
			emit(streamEvent)
		}
	})
}
