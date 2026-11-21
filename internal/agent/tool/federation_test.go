package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/mcp"
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
				Group:       "mcp:remote",
			},
		},
	}
	provider := NewFederationProvider(nil, source)

	tools, err := provider.Tools(context.Background(), SessionContext{BotID: "bot-1"})
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "remote_observe" || tools[0].Group != "mcp:remote" {
		t.Fatalf("Tools() = %#v, want only non-built-in federated tool in group mcp:remote", tools)
	}
}

func TestFederationProviderGroupsConnectorToolsBySourceInsteadOfToolNameVerb(t *testing.T) {
	t.Parallel()

	source := &federationTestSource{
		tools: []mcp.ToolDescriptor{
			{
				Name:        "list_email",
				Description: "List emails in the mailbox.",
				InputSchema: emptyObjectSchema(),
				Group:       "connector:email",
			},
			{
				Name:        "list_email_accounts",
				Description: "List configured email accounts.",
				InputSchema: emptyObjectSchema(),
				Group:       "connector:email",
			},
			{
				Name:        "send_email",
				Description: "Send an email message.",
				InputSchema: emptyObjectSchema(),
				Group:       "connector:email",
			},
			{
				Name:        "get_issue",
				Description: "Get a repository issue by number.",
				InputSchema: emptyObjectSchema(),
				Group:       "connector:github",
			},
			{
				Name:        "unscoped_action",
				Description: "Tool from a source that left Group empty.",
				InputSchema: emptyObjectSchema(),
			},
		},
	}
	provider := NewFederationProvider(nil, source)

	tools, err := provider.Tools(context.Background(), SessionContext{BotID: "bot-1"})
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	wantGroups := map[string]string{
		"list_email":          "connector:email",
		"list_email_accounts": "connector:email",
		"send_email":          "connector:email",
		"get_issue":           "connector:github",
		"unscoped_action":     "mcp",
	}
	if len(tools) != len(wantGroups) {
		t.Fatalf("len(tools) = %d, want %d", len(tools), len(wantGroups))
	}
	for _, tool := range tools {
		if want := wantGroups[tool.Name]; tool.Group != want {
			t.Fatalf("tool %q Group = %q, want %q", tool.Name, tool.Group, want)
		}
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
