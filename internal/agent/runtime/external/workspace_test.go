package external

import (
	"errors"
	"net/http"
	"testing"

	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestDirectRuntimeRejectsRemoteWorkspaceBeforeExecution(t *testing.T) {
	err := RequireContainerWorkspace(bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendRemote, DefaultWorkDir: "/Users/alice/workspace"}, "codex")
	var feedback *agentfeedback.Error
	if !errors.As(err, &feedback) || feedback.Code != agentfeedback.CodeNoWorkspaceExec || feedback.Reason != "remote_workspace_unsupported" || feedback.HTTPStatus != http.StatusConflict {
		t.Fatalf("remote execution should give actionable workspace feedback: %v", err)
	}
	if err := RequireContainerWorkspace(bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer}, "codex"); err != nil {
		t.Fatalf("container runtime rejected: %v", err)
	}
}
