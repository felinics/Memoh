package federation

import (
	"context"
	"errors"
	"log/slog"
	"testing"

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
