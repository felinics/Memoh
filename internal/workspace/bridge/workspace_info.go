package bridge

import "context"

const (
	WorkspaceBackendContainer = "container"
	WorkspaceBackendRemote    = "remote"
	ACPToolsProxyAddr         = "127.0.0.1:18732"
	ACPToolsProxyHTTPURL      = "http://" + ACPToolsProxyAddr + "/mcp"
)

type WorkspaceInfo struct {
	Backend         string
	OS              string
	DefaultWorkDir  string
	ACPToolsHTTPURL string
	Capabilities    []string
	TargetID        string
	TargetKind      string
	TargetName      string
}

type WorkspaceInfoProvider interface {
	WorkspaceInfo(ctx context.Context, botID string) (WorkspaceInfo, error)
}
