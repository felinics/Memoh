package native

import (
	"context"
	"testing"

	"github.com/memohai/memoh/internal/workspace/bridge"
)

type hookWorkspaceDescriptorProvider struct {
	descriptorCalls int
	runtimeCalls    int
}

func (*hookWorkspaceDescriptorProvider) MCPClient(context.Context, string) (*bridge.Client, error) {
	return nil, nil
}

func (p *hookWorkspaceDescriptorProvider) WorkspaceDescriptorInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	p.descriptorCalls++
	return bridge.WorkspaceInfo{
		Backend:        bridge.WorkspaceBackendRemote,
		DefaultWorkDir: "/workspace/project",
	}, nil
}

func (p *hookWorkspaceDescriptorProvider) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	p.runtimeCalls++
	return bridge.WorkspaceInfo{
		Backend:        bridge.WorkspaceBackendContainer,
		DefaultWorkDir: "/data",
	}, nil
}

func TestHookWorkspaceUsesDescriptorWithoutOpeningRuntime(t *testing.T) {
	provider := &hookWorkspaceDescriptorProvider{}
	agent := &Agent{bridgeProvider: provider}

	got := agent.hookWorkspace(t.Context(), workspaceContextTestBotID)
	if got.Runtime != bridge.WorkspaceBackendRemote || got.CWD != "/workspace/project" {
		t.Fatalf("hook workspace = %#v", got)
	}
	if provider.descriptorCalls != 1 {
		t.Fatalf("descriptor calls = %d, want 1", provider.descriptorCalls)
	}
	if provider.runtimeCalls != 0 {
		t.Fatalf("runtime calls = %d, want 0", provider.runtimeCalls)
	}
}

const workspaceContextTestBotID = "6e33a6ad-f888-4051-9b1a-e709bdc048b2"
