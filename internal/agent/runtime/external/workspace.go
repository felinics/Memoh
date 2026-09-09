package external

import (
	"net/http"

	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// RequireContainerWorkspace guards drivers whose executable environment and
// credential paths assume the container layout. The remote bridge maps file
// RPC paths, but it does not translate paths embedded in process environments.
func RequireContainerWorkspace(info bridge.WorkspaceInfo, runtime string) error {
	if info.Backend != bridge.WorkspaceBackendRemote {
		return nil
	}
	return agentfeedback.New(
		agentfeedback.CodeNoWorkspaceExec,
		"remote_workspace_unsupported",
		http.StatusConflict,
		"",
		"This direct Agent runtime requires a container workspace. Select the bot's container workspace before starting it.",
		map[string]string{"runtime": runtime, "workspace_backend": info.Backend},
	)
}
