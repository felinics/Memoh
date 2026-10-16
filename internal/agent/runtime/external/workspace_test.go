package external

import (
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestDirectRuntimeRejectsRemoteWorkspaceBeforeExecution(t *testing.T) {
	err := RequireContainerWorkspace(bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendRemote, DefaultWorkDir: "/Users/alice/workspace"}, "codex")
	if !errors.Is(err, ErrContainerWorkspaceRequired) {
		t.Fatalf("remote execution error = %v, want ErrContainerWorkspaceRequired", err)
	}
	if err := RequireContainerWorkspace(bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer}, "codex"); err != nil {
		t.Fatalf("container runtime rejected: %v", err)
	}
}
