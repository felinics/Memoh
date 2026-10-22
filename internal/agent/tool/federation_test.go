package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/mcp"
	federation "github.com/felinics/memoh/internal/mcp/sources/federation"
)

type federationTestSource struct {
	tools []mcp.ToolDescriptor
	calls []string
}

func (s *federationTestSource) ListTools(context.Context, mcp.ToolSessionContext) ([]mcp.ToolDescriptor, error) {
	return append([]mcp.ToolDescriptor(nil), s.tools...), nil
}

func (s *federationTestSource) CallTool(_ context.Context, _ mcp.ToolSessionContext, toolName string, _ map[string]any) (map[string]any, error) {
	s.calls = append(s.calls, toolName)
	return mcp.BuildToolSuccessResult(map[string]any{"tool": toolName}), nil
}

type partialFederationSource struct {
	federationTestSource
	err error
}

func (s *partialFederationSource) ListTools(_ context.Context, _ mcp.ToolSessionContext) ([]mcp.ToolDescriptor, error) {
	return append([]mcp.ToolDescriptor(nil), s.tools...), s.err
}

type failingFederationSource struct {
	federationTestSource
	err         error
	diagnostics []string
}

func (s *failingFederationSource) ListTools(context.Context, mcp.ToolSessionContext) ([]mcp.ToolDescriptor, error) {
	return nil, s.err
}

func (s *failingFederationSource) ToolDiagnostics(context.Context, mcp.ToolSessionContext) []string {
	return s.diagnostics
}

func TestFederationProviderSurfacesListingFailureAsNotice(t *testing.T) {
	t.Parallel()

	source := &failingFederationSource{
		err:         errors.New("connect-it: tool_discovery_failed (HTTP 502)"),
		diagnostics: []string{"  Connector tools could not be loaded: Forbidden  ", "", "Other tools work."},
	}
	provider := NewFederationProvider(nil, source)
	session := SessionContext{BotID: "bot-1"}

	tools, err := provider.Tools(context.Background(), session)
	if err != nil || len(tools) != 0 {
		t.Fatalf("a failed listing must not fail the turn: tools=%v err=%v", tools, err)
	}
	want := "Connector tools could not be loaded: Forbidden\n\nOther tools work."
	if got := provider.ToolNotice(context.Background(), session); got != want {
		t.Fatalf("ToolNotice() = %q, want %q", got, want)
	}

	plain := NewFederationProvider(nil, &federationTestSource{})
	if got := plain.ToolNotice(context.Background(), session); got != "" {
		t.Fatalf("a source without diagnostics must contribute no notice, got %q", got)
	}
}

func TestFederationProviderSkipsBuiltInNameCollisions(t *testing.T) {
	t.Parallel()

	source := &federationTestSource{
		tools: []mcp.ToolDescriptor{
			{
				Name:        ToolBrowserObserve().String(),
				Description: "Federated collision",
				InputSchema: emptyObjectSchema(),
			},
			{
				Name:        "remote_observe",
				Description: "Federated remote tool",
				InputSchema: emptyObjectSchema(),
			},
		},
	}
	provider := NewFederationProvider(nil, source)

	tools, err := provider.Tools(context.Background(), SessionContext{BotID: "bot-1"})
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "remote_observe" {
		t.Fatalf("Tools() = %#v, want only non-built-in federated tool", tools)
	}
}

func TestNormalizeMCPResultExposesReauthorization(t *testing.T) {
	structured := map[string]any{
		"error":   "reauth_required",
		"message": "connection needs reauthorization",
	}
	result := normalizeMCPResult(map[string]any{
		"isError":           true,
		"structuredContent": structured,
	})
	got, ok := result.(map[string]any)
	if !ok || got["error"] != "reauth_required" ||
		got["message"] != "connection needs reauthorization" {
		t.Fatalf("normalizeMCPResult() = %#v, want structured reauthorization error", result)
	}
}

// A toolset change between turns fires the hook exactly once per change
// (Context Epoch: 工具集变了记 toolset-change 翻篇); stable toolsets and
// degraded listings never fire it.
func TestFederationProviderToolsetChangeHook(t *testing.T) {
	t.Parallel()

	source := &federationTestSource{tools: []mcp.ToolDescriptor{
		{Name: "search", Description: "v1", InputSchema: map[string]any{"type": "object"}},
	}}
	var fired []string
	provider := NewFederationProvider(nil, source, WithToolsetChangeHook(func(_ context.Context, sessionID string) {
		fired = append(fired, sessionID)
	}))
	session := SessionContext{BotID: "bot-1", SessionID: "sess-1"}

	// First observation only establishes the baseline.
	if _, err := provider.Tools(context.Background(), session); err != nil {
		t.Fatalf("tools: %v", err)
	}
	if len(fired) != 0 {
		t.Fatalf("the first observation must not fire the hook: %v", fired)
	}

	// An unchanged toolset stays silent.
	if _, err := provider.Tools(context.Background(), session); err != nil {
		t.Fatalf("tools: %v", err)
	}
	if len(fired) != 0 {
		t.Fatalf("an unchanged toolset must not fire the hook: %v", fired)
	}

	// A changed toolset books exactly one change.
	source.tools = []mcp.ToolDescriptor{
		{Name: "search", Description: "v2", InputSchema: map[string]any{"type": "object"}},
	}
	if _, err := provider.Tools(context.Background(), session); err != nil {
		t.Fatalf("tools: %v", err)
	}
	if len(fired) != 1 || fired[0] != "sess-1" {
		t.Fatalf("hook fired = %v, want exactly one call for sess-1", fired)
	}

	// Degraded listings never fire: an outage is not a toolset change.
	degraded := &failingFederationSource{err: errors.New("connection refused")}
	degraded.tools = source.tools
	provider = NewFederationProvider(nil, degraded, WithToolsetChangeHook(func(_ context.Context, sessionID string) {
		fired = append(fired, sessionID)
	}))
	if _, err := provider.Tools(context.Background(), session); err != nil {
		t.Fatalf("tools: %v", err)
	}
	if len(fired) != 1 {
		t.Fatalf("a degraded listing must not fire the hook: %v", fired)
	}
}

// A degraded listing that still returns best-effort tools (the federation
// source's per-connection last-good snapshot) keeps those tools available and
// never books a toolset change: the rendered bytes did not change.
func TestFederationProviderKeepsBestEffortToolsOnDegradedListing(t *testing.T) {
	t.Parallel()

	source := &partialFederationSource{
		federationTestSource: federationTestSource{tools: []mcp.ToolDescriptor{
			{Name: "search", Description: "v1", InputSchema: map[string]any{"type": "object"}},
		}},
		err: federation.ErrToolListingDegraded,
	}
	fired := 0
	provider := NewFederationProvider(nil, source, WithToolsetChangeHook(func(context.Context, string) { fired++ }))
	session := SessionContext{BotID: "bot-1", SessionID: "sess-1"}

	tools, err := provider.Tools(context.Background(), session)
	if err != nil {
		t.Fatalf("a degraded listing must not fail the turn: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "search" {
		t.Fatalf("best-effort tools dropped: %+v", tools)
	}
	if fired != 0 {
		t.Fatalf("a degraded listing must not fire the toolset hook (%d)", fired)
	}
}
