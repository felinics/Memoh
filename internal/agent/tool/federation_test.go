package tools

import (
	"context"
	"testing"

	"github.com/memohai/memoh/internal/mcp"
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
