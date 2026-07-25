package core

import (
	"context"
	"io"

	"github.com/memohai/memoh/domains/media/storage"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
)

// bridgeContainerFileClientProvider adapts Runtime bridge.Provider to the
// Media-owned container file capability port. Kept in composition so Media
// storage ports never depend on bridge concrete or protobuf types.
type bridgeContainerFileClientProvider struct {
	provider bridge.Provider
}

func newBridgeContainerFileClientProvider(provider bridge.Provider) storage.ContainerFileClientProvider {
	return bridgeContainerFileClientProvider{provider: provider}
}

func (p bridgeContainerFileClientProvider) ContainerFileClient(ctx context.Context, botID string) (storage.ContainerFileClient, error) {
	client, err := p.provider.MCPClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	return bridgeContainerFileClient{client: client}, nil
}

type bridgeContainerFileClient struct {
	client *bridge.Client
}

func (c bridgeContainerFileClient) WriteRaw(ctx context.Context, path string, r io.Reader) (int64, error) {
	return c.client.WriteRaw(ctx, path, r)
}

func (c bridgeContainerFileClient) ReadRaw(ctx context.Context, path string) (io.ReadCloser, error) {
	return c.client.ReadRaw(ctx, path)
}

func (c bridgeContainerFileClient) DeleteFile(ctx context.Context, path string, recursive bool) error {
	return c.client.DeleteFile(ctx, path, recursive)
}

func (c bridgeContainerFileClient) ListDirAll(ctx context.Context, path string, recursive bool) ([]storage.FileEntry, error) {
	entries, err := c.client.ListDirAll(ctx, path, recursive)
	if err != nil {
		return nil, err
	}
	out := make([]storage.FileEntry, len(entries))
	for i, e := range entries {
		if e == nil {
			continue
		}
		out[i] = storage.FileEntry{Path: e.GetPath(), IsDir: e.GetIsDir()}
	}
	return out, nil
}
