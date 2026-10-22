package federation

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	mcpgw "github.com/felinics/memoh/internal/mcp"
)

type testConnectionLister struct {
	items []mcpgw.Connection
	err   error
}

func (l *testConnectionLister) ListActiveByBot(_ context.Context, _ string) ([]mcpgw.Connection, error) {
	if l.err != nil {
		return nil, l.err
	}
	return l.items, nil
}

type testGateway struct {
	listHTTP  []mcpgw.ToolDescriptor
	listSSE   []mcpgw.ToolDescriptor
	listStdio []mcpgw.ToolDescriptor

	lastCallType string
}

func (g *testGateway) ListHTTPConnectionTools(_ context.Context, _ mcpgw.Connection) ([]mcpgw.ToolDescriptor, error) {
	return g.listHTTP, nil
}

func (g *testGateway) CallHTTPConnectionTool(_ context.Context, _ mcpgw.Connection, _ string, _ map[string]any) (map[string]any, error) {
	g.lastCallType = "http"
	return map[string]any{"result": map[string]any{"ok": true, "route": "http"}}, nil
}

func (g *testGateway) ListSSEConnectionTools(_ context.Context, _ mcpgw.Connection) ([]mcpgw.ToolDescriptor, error) {
	return g.listSSE, nil
}

func (g *testGateway) CallSSEConnectionTool(_ context.Context, _ mcpgw.Connection, _ string, _ map[string]any) (map[string]any, error) {
	g.lastCallType = "sse"
	return map[string]any{"result": map[string]any{"ok": true, "route": "sse"}}, nil
}

func (g *testGateway) ListStdioConnectionTools(_ context.Context, _ string, _ mcpgw.Connection) ([]mcpgw.ToolDescriptor, error) {
	return g.listStdio, nil
}

func (g *testGateway) CallStdioConnectionTool(_ context.Context, _ string, _ mcpgw.Connection, _ string, _ map[string]any) (map[string]any, error) {
	g.lastCallType = "stdio"
	return map[string]any{"result": map[string]any{"ok": true, "route": "stdio"}}, nil
}

func TestSourceListToolsIncludesSSETools(t *testing.T) {
	gateway := &testGateway{
		listSSE: []mcpgw.ToolDescriptor{
			{
				Name:        "search",
				Description: "search remote data",
				InputSchema: map[string]any{"type": "object"},
			},
		},
	}
	lister := &testConnectionLister{
		items: []mcpgw.Connection{
			{
				ID:     "conn-1",
				Name:   "Remote SSE",
				Type:   "sse",
				Active: true,
				Config: map[string]any{"url": "http://example.com/sse"},
			},
		},
	}

	source := NewSource(slog.Default(), gateway, lister)
	tools, err := source.ListTools(context.Background(), mcpgw.ToolSessionContext{BotID: "bot-1"})
	if err != nil {
		t.Fatalf("list tools failed: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "remote_sse_search" {
		t.Fatalf("unexpected tool alias: %s", tools[0].Name)
	}
}

func TestSourceCallToolRoutesToSSEConnection(t *testing.T) {
	gateway := &testGateway{
		listSSE: []mcpgw.ToolDescriptor{
			{
				Name:        "search",
				Description: "search remote data",
				InputSchema: map[string]any{"type": "object"},
			},
		},
	}
	lister := &testConnectionLister{
		items: []mcpgw.Connection{
			{
				ID:     "conn-1",
				Name:   "Remote SSE",
				Type:   "sse",
				Active: true,
				Config: map[string]any{"url": "http://example.com/sse"},
			},
		},
	}
	source := NewSource(slog.Default(), gateway, lister)

	result, err := source.CallTool(context.Background(), mcpgw.ToolSessionContext{BotID: "bot-1"}, "remote_sse_search", map[string]any{"query": "hello"})
	if err != nil {
		t.Fatalf("call tool failed: %v", err)
	}
	if gateway.lastCallType != "sse" {
		t.Fatalf("expected sse route, got: %s", gateway.lastCallType)
	}
	if ok, _ := result["ok"].(bool); !ok {
		t.Fatalf("expected ok=true in result")
	}
}

func TestSourceCallToolRechecksRuntimeGuardBeforeFederatedEffect(t *testing.T) {
	guardErr := errors.New("runtime ownership lost")
	gateway := &testGateway{listSSE: []mcpgw.ToolDescriptor{{
		Name: "search", InputSchema: map[string]any{"type": "object"},
	}}}
	lister := &testConnectionLister{items: []mcpgw.Connection{{
		ID: "conn-guard", Name: "Remote SSE", Type: "sse", Active: true,
		Config: map[string]any{"url": "http://example.com/sse"},
	}}}
	source := NewSource(slog.Default(), gateway, lister)
	if _, err := source.ListTools(context.Background(), mcpgw.ToolSessionContext{BotID: "bot-1"}); err != nil {
		t.Fatalf("prime routes: %v", err)
	}

	_, err := source.CallTool(context.Background(), mcpgw.ToolSessionContext{
		BotID: "bot-1", RuntimeGuard: func(context.Context) error { return guardErr },
	}, "remote_sse_search", nil)
	if !errors.Is(err, guardErr) {
		t.Fatalf("CallTool() error = %v, want runtime guard error", err)
	}
	if gateway.lastCallType != "" {
		t.Fatalf("federated effect executed through %q after guard rejection", gateway.lastCallType)
	}
}

func TestSourceRenamesReservedToolAliases(t *testing.T) {
	gateway := &testGateway{
		listSSE: []mcpgw.ToolDescriptor{
			{
				Name:        "observe",
				Description: "observe browser state",
				InputSchema: map[string]any{"type": "object"},
			},
		},
	}
	lister := &testConnectionLister{
		items: []mcpgw.Connection{
			{
				ID:     "conn-1",
				Name:   "browser",
				Type:   "sse",
				Active: true,
				Config: map[string]any{"url": "http://example.com/sse"},
			},
		},
	}
	source := NewSource(slog.Default(), gateway, lister, WithReservedToolName(func(name string) bool {
		return name == "browser_observe"
	}))

	tools, err := source.ListTools(context.Background(), mcpgw.ToolSessionContext{BotID: "bot-1"})
	if err != nil {
		t.Fatalf("list tools failed: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "browser_observe_2" {
		t.Fatalf("tools = %#v, want reserved alias renamed to browser_observe_2", tools)
	}

	result, err := source.CallTool(context.Background(), mcpgw.ToolSessionContext{BotID: "bot-1"}, "browser_observe_2", map[string]any{})
	if err != nil {
		t.Fatalf("call renamed tool failed: %v", err)
	}
	if route, _ := result["route"].(string); route != "sse" {
		t.Fatalf("result = %#v, want sse route", result)
	}
}

func TestCachedRouteCannotCallRemovedOrReconfiguredConnection(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "removed-or-disabled", true: "reconfigured"}[changed], func(t *testing.T) {
			gateway := &testGateway{listHTTP: []mcpgw.ToolDescriptor{{Name: "search"}}}
			lister := &testConnectionLister{items: []mcpgw.Connection{{ID: "connection", Name: "Remote", Type: "http", Active: true, Config: map[string]any{"url": "https://example.com/mcp"}}}}
			source := NewSource(slog.Default(), gateway, lister)
			session := mcpgw.ToolSessionContext{BotID: "bot"}
			if _, err := source.ListTools(t.Context(), session); err != nil {
				t.Fatal(err)
			}
			if changed {
				lister.items[0].UpdatedAt = time.Now()
			} else {
				lister.items = nil
			}
			if _, err := source.CallTool(t.Context(), session, "remote_search", nil); !errors.Is(err, mcpgw.ErrToolNotFound) {
				t.Fatalf("stale call error: %v", err)
			}
			if gateway.lastCallType != "" {
				t.Fatal("stale route reached remote server")
			}
		})
	}
}

// flakyListGateway fails SSE tool listings on demand.
type flakyListGateway struct {
	testGateway
	failSSE bool
}

func (g *flakyListGateway) ListSSEConnectionTools(ctx context.Context, conn mcpgw.Connection) ([]mcpgw.ToolDescriptor, error) {
	if g.failSSE {
		return nil, errors.New("connection refused")
	}
	return g.testGateway.ListSSEConnectionTools(ctx, conn)
}

func sseTestConnection() mcpgw.Connection {
	return mcpgw.Connection{
		ID:     "conn-1",
		Name:   "Remote SSE",
		Type:   "sse",
		Active: true,
		Config: map[string]any{"url": "http://example.com/sse"},
	}
}

// A connection that listed successfully before keeps contributing its last
// successful tool listing while it is unreachable: one MCP server outage must
// not rewrite the agent's tool documentation (Context Epoch: 工具列表失败沿用
// 旧版).
func TestSourceListToolsFallsBackToLastSuccessfulListing(t *testing.T) {
	gateway := &flakyListGateway{testGateway: testGateway{
		listSSE: []mcpgw.ToolDescriptor{{Name: "search", Description: "search remote data", InputSchema: map[string]any{"type": "object"}}},
	}}
	lister := &testConnectionLister{items: []mcpgw.Connection{sseTestConnection()}}
	source := NewSource(slog.Default(), gateway, lister)
	session := mcpgw.ToolSessionContext{BotID: "bot-1"}

	tools, err := source.ListTools(context.Background(), session)
	if err != nil || len(tools) != 1 || tools[0].Name != "remote_sse_search" {
		t.Fatalf("initial listing = %+v, %v", tools, err)
	}

	gateway.failSSE = true
	source.Invalidate("bot-1")
	tools, err = source.ListTools(context.Background(), session)
	if err != nil {
		t.Fatalf("a covered outage must not degrade the listing: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "remote_sse_search" || tools[0].Description != "[Remote SSE] search remote data" {
		t.Fatalf("fallback tools = %+v; want the last successful listing byte for byte", tools)
	}

	// The fallback tools remain callable through their cached routes.
	result, err := source.CallTool(context.Background(), session, "remote_sse_search", nil)
	if err != nil {
		t.Fatalf("call through fallback route: %v", err)
	}
	if result["route"] != "sse" {
		t.Fatalf("fallback call routed to %v", result)
	}
}

// A connection that never listed successfully has no snapshot: the listing is
// degraded so registry-caching callers (the tool gateway) serve their own
// last-good registry instead of caching the partial list.
func TestSourceListToolsDegradedWhenConnectionNeverListed(t *testing.T) {
	gateway := &flakyListGateway{
		testGateway: testGateway{listSSE: []mcpgw.ToolDescriptor{{Name: "search"}}},
		failSSE:     true,
	}
	lister := &testConnectionLister{items: []mcpgw.Connection{sseTestConnection()}}
	source := NewSource(slog.Default(), gateway, lister)
	session := mcpgw.ToolSessionContext{BotID: "bot-1"}

	_, err := source.ListTools(context.Background(), session)
	if !errors.Is(err, ErrToolListingDegraded) {
		t.Fatalf("uncovered outage err = %v, want ErrToolListingDegraded", err)
	}

	// The degraded marker survives the short-TTL cache: a cached partial
	// listing must not turn into a cacheable success.
	_, err = source.ListTools(context.Background(), session)
	if !errors.Is(err, ErrToolListingDegraded) {
		t.Fatalf("cached degraded listing err = %v, want ErrToolListingDegraded", err)
	}

	// Once the connection recovers, the listing is complete again.
	gateway.failSSE = false
	source.Invalidate("bot-1")
	tools, err := source.ListTools(context.Background(), session)
	if err != nil || len(tools) != 1 {
		t.Fatalf("recovered listing = %+v, %v", tools, err)
	}
}

// Losing the connection list cannot rebuild anything — deleted connections
// must not resurrect — so the bot's last COMPLETE listing is served instead:
// its bytes are stable, and CallTool's connection revalidation fails closed
// while the lister is down. With no complete listing on record the result is
// degraded.
func TestSourceListToolsConnectionsListFailureIsDegraded(t *testing.T) {
	gateway := &flakyListGateway{testGateway: testGateway{
		listSSE: []mcpgw.ToolDescriptor{{Name: "search", Description: "search remote data"}},
	}}
	lister := &testConnectionLister{items: []mcpgw.Connection{sseTestConnection()}}
	source := NewSource(slog.Default(), gateway, lister)
	session := mcpgw.ToolSessionContext{BotID: "bot-1"}
	if _, err := source.ListTools(context.Background(), session); err != nil {
		t.Fatalf("initial listing: %v", err)
	}

	lister.err = errors.New("db down")
	lister.items = nil
	source.Invalidate("bot-1")
	tools, err := source.ListTools(context.Background(), session)
	if err != nil {
		t.Fatalf("a bot with a complete last-good listing must not degrade: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "remote_sse_search" {
		t.Fatalf("last-good tools = %+v", tools)
	}

	// A bot that never completed a listing has nothing to fall back to.
	fresh := NewSource(slog.Default(), gateway, &testConnectionLister{err: errors.New("db down")})
	if _, err := fresh.ListTools(context.Background(), mcpgw.ToolSessionContext{BotID: "bot-2"}); !errors.Is(err, ErrToolListingDegraded) {
		t.Fatalf("no last-good listing err = %v, want ErrToolListingDegraded", err)
	}
}

// A reconfigured connection (UpdatedAt changed) invalidates its fallback
// snapshot: the old tools may belong to a different endpoint and must not be
// served — let alone routed — against the new configuration.
func TestSourceFallbackDoesNotSurviveReconfiguration(t *testing.T) {
	gateway := &flakyListGateway{testGateway: testGateway{
		listSSE: []mcpgw.ToolDescriptor{{Name: "search", Description: "search remote data"}},
	}}
	connection := sseTestConnection()
	connection.UpdatedAt = time.UnixMilli(1000)
	lister := &testConnectionLister{items: []mcpgw.Connection{connection}}
	source := NewSource(slog.Default(), gateway, lister)
	session := mcpgw.ToolSessionContext{BotID: "bot-1"}
	if _, err := source.ListTools(context.Background(), session); err != nil {
		t.Fatalf("initial listing: %v", err)
	}

	// Reconfigure the connection (new endpoint bumps UpdatedAt), then fail
	// the next listing: the old snapshot must NOT bind to the new endpoint.
	connection.Config = map[string]any{"url": "http://evil.example.com/sse"}
	connection.UpdatedAt = time.UnixMilli(2000)
	lister.items = []mcpgw.Connection{connection}
	gateway.failSSE = true
	source.Invalidate("bot-1")
	tools, err := source.ListTools(context.Background(), session)
	if !errors.Is(err, ErrToolListingDegraded) {
		t.Fatalf("reconfigured+failed listing err = %v, want ErrToolListingDegraded", err)
	}
	if len(tools) != 0 {
		t.Fatalf("stale snapshot bound to the new endpoint: %+v", tools)
	}
	if _, err := source.CallTool(context.Background(), session, "remote_sse_search", nil); !errors.Is(err, mcpgw.ErrToolNotFound) {
		t.Fatalf("stale tool must not be callable against the new endpoint: %v", err)
	}
}
