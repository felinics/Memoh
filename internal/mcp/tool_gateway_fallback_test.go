package mcp

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// flakyToolSource serves a successful tool list until told to fail, then
// fails until told to recover.
type flakyToolSource struct {
	mu      sync.Mutex
	tools   []ToolDescriptor
	failing bool
}

func (f *flakyToolSource) ListTools(_ context.Context, _ ToolSessionContext) ([]ToolDescriptor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing {
		return nil, errors.New("mcp backend unreachable")
	}
	return f.tools, nil
}

func (*flakyToolSource) CallTool(_ context.Context, _ ToolSessionContext, _ string, _ map[string]any) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}

func (f *flakyToolSource) set(tools []ToolDescriptor, failing bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tools = tools
	f.failing = failing
}

func gatewayTestSession(runID string) ToolSessionContext {
	return ToolSessionContext{BotID: "bot-1", ChatID: "chat-1", SessionID: "sess-1", RunID: runID}
}

func toolNames(tools []ToolDescriptor) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestToolRegistryRefreshFailureKeepsLastSuccessfulList(t *testing.T) {
	t.Parallel()

	source := &flakyToolSource{tools: []ToolDescriptor{{Name: "tool-a", InputSchema: map[string]any{}}}}
	svc := NewToolGatewayService(slog.New(slog.DiscardHandler), []ToolSource{source})
	svc.cacheTTL = time.Millisecond // expire fast so every call refreshes

	// Turn 1: the source is healthy and the registry is cached.
	first, err := svc.ListTools(context.Background(), gatewayTestSession("run-1"))
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if got := toolNames(first); len(got) != 1 || got[0] != "tool-a" {
		t.Fatalf("tools = %v, want [tool-a]", got)
	}

	// Turn 2 (another run): the source fails mid-session. The gateway must
	// keep serving the last successful list instead of swapping in an empty
	// or partial one (Context Epoch spec: 工具列表刷新失败沿用旧版).
	source.set(nil, true)
	second, err := svc.ListTools(context.Background(), gatewayTestSession("run-2"))
	if err != nil {
		t.Fatalf("ListTools during outage: %v", err)
	}
	if got := toolNames(second); len(got) != 1 || got[0] != "tool-a" {
		t.Fatalf("tools during outage = %v, want the last successful [tool-a]", got)
	}

	// The source recovers with a genuinely different list: the next refresh
	// serves the new list (that change is a real toolset change, not a
	// failure artifact).
	source.set([]ToolDescriptor{{Name: "tool-b", InputSchema: map[string]any{}}}, false)
	third, err := svc.ListTools(context.Background(), gatewayTestSession("run-3"))
	if err != nil {
		t.Fatalf("ListTools after recovery: %v", err)
	}
	if got := toolNames(third); len(got) != 1 || got[0] != "tool-b" {
		t.Fatalf("tools after recovery = %v, want [tool-b]", got)
	}
}

func TestToolRegistryRefreshFailureWithoutPriorSuccessServesUncachedPartial(t *testing.T) {
	t.Parallel()

	failing := &flakyToolSource{failing: true}
	healthy := &flakyToolSource{tools: []ToolDescriptor{{Name: "tool-a", InputSchema: map[string]any{}}}}
	svc := NewToolGatewayService(slog.New(slog.DiscardHandler), []ToolSource{failing, healthy})
	svc.cacheTTL = time.Hour // within TTL: a cached degraded registry would stick

	session := gatewayTestSession("run-1")
	first, err := svc.ListTools(context.Background(), session)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if got := toolNames(first); len(got) != 1 || got[0] != "tool-a" {
		t.Fatalf("tools = %v, want the partial [tool-a]", got)
	}

	// The partial registry must not be cached: once the failing source
	// recovers, the very next call — same session, still inside the TTL —
	// sees the full list.
	failing.set([]ToolDescriptor{{Name: "tool-b", InputSchema: map[string]any{}}}, false)
	second, err := svc.ListTools(context.Background(), session)
	if err != nil {
		t.Fatalf("ListTools after recovery: %v", err)
	}
	if got := toolNames(second); len(got) != 2 {
		t.Fatalf("tools after recovery = %v, want both tools (degraded registry must not be cached)", got)
	}
}
