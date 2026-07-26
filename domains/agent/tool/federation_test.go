package tool

import (
	"context"
	"testing"

	"github.com/memohai/memoh/domains/agent/mcp"
	mcppersistence "github.com/memohai/memoh/domains/agent/mcp/persistence"
)

type federationTestSource struct {
	tools []mcppersistence.ToolDescriptor
	calls []string
}

func (s *federationTestSource) ListTools(context.Context, mcp.ToolSessionContext) ([]mcppersistence.ToolDescriptor, error) {
	return append([]mcppersistence.ToolDescriptor(nil), s.tools...), nil
}

func (s *federationTestSource) CallTool(_ context.Context, _ mcp.ToolSessionContext, toolName string, _ map[string]any) (map[string]any, error) {
	s.calls = append(s.calls, toolName)
	return mcp.BuildToolSuccessResult(map[string]any{"tool": toolName}), nil
}

func TestFederationProviderSkipsBuiltInNameCollisions(t *testing.T) {
	t.Parallel()

	source := &federationTestSource{
		tools: []mcppersistence.ToolDescriptor{
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
