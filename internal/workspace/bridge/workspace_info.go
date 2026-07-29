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
}

type WorkspaceInfoProvider interface {
	WorkspaceInfo(ctx context.Context, botID string) (WorkspaceInfo, error)
}

// WorkspaceDescriptorInfoProvider supplies routing metadata without requiring
// the underlying workspace runtime to be online.
type WorkspaceDescriptorInfoProvider interface {
	WorkspaceDescriptorInfo(ctx context.Context, botID string) (WorkspaceInfo, error)
}
