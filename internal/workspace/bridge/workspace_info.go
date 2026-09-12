package bridge

import "context"

const (
	WorkspaceBackendContainer = "container"
	WorkspaceBackendRemote    = "remote"
	ToolsProxyAddr            = "127.0.0.1:18732"
	ToolsProxyHTTPURL         = "http://" + ToolsProxyAddr + "/mcp"
)

type WorkspaceInfo struct {
	Backend        string
	OS             string
	DefaultWorkDir string
	ToolsHTTPURL   string
}

type WorkspaceInfoProvider interface {
	WorkspaceInfo(ctx context.Context, botID string) (WorkspaceInfo, error)
}
