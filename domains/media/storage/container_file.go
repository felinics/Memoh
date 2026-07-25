package storage

import (
	"context"
	"io"
)

// FileEntry is a narrow directory listing DTO for container-backed storage.
// It deliberately omits Runtime bridge/protobuf types.
type FileEntry struct {
	Path  string
	IsDir bool
}

// ContainerFileClient is the file capability needed by container-backed
// Media storage providers. Composition adapts Runtime bridge clients to this port.
type ContainerFileClient interface {
	WriteRaw(ctx context.Context, path string, r io.Reader) (int64, error)
	ReadRaw(ctx context.Context, path string) (io.ReadCloser, error)
	DeleteFile(ctx context.Context, path string, recursive bool) error
	ListDirAll(ctx context.Context, path string, recursive bool) ([]FileEntry, error)
}

// ContainerFileClientProvider resolves a per-bot ContainerFileClient.
type ContainerFileClientProvider interface {
	ContainerFileClient(ctx context.Context, botID string) (ContainerFileClient, error)
}
