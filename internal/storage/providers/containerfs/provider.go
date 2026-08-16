// Package containerfs implements storage.Provider for bot containers
// backed by gRPC calls to the in-container MCP service. Files are stored
// inside the container's writable layer at /data/.memoh/media/<subpath>.
package containerfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	attachmentpkg "github.com/memohai/memoh/internal/attachment"
	"github.com/memohai/memoh/internal/workspace/bridge"
)

const containerMediaRoot = ".memoh/media"

// Provider stores media assets inside bot containers via gRPC.
type Provider struct {
	clients bridge.Provider
}

// New creates a container-based storage provider.
func New(clients bridge.Provider) *Provider {
	return &Provider{clients: clients}
}

// Put writes data to the bot container via gRPC streaming.
func (p *Provider) Put(ctx context.Context, key string, reader io.Reader) error {
	botID, sub, err := parseRoutingKey(key)
	if err != nil {
		return err
	}
	client, err := p.clients.MCPClient(ctx, botID)
	if err != nil {
		return fmt.Errorf("get client: %w", err)
	}
	containerPath := filepath.Join(containerMediaRoot, sub)
	if _, err := client.WriteRaw(ctx, containerPath, reader); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

// Open reads a file from the bot container via gRPC streaming.
func (p *Provider) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	botID, sub, err := parseRoutingKey(key)
	if err != nil {
		return nil, err
	}
	client, err := p.clients.MCPClient(ctx, botID)
	if err != nil {
		return nil, fmt.Errorf("get client: %w", err)
	}
	containerPath := filepath.Join(containerMediaRoot, sub)
	return client.ReadRaw(ctx, containerPath)
}

// Delete removes a file from the bot container.
func (p *Provider) Delete(ctx context.Context, key string) error {
	botID, sub, err := parseRoutingKey(key)
	if err != nil {
		return err
	}
	client, err := p.clients.MCPClient(ctx, botID)
	if err != nil {
		return fmt.Errorf("get client: %w", err)
	}
	containerPath := filepath.Join(containerMediaRoot, sub)
	return client.DeleteFile(ctx, containerPath, false)
}

// AccessPath returns the stable path in the workspace filesystem namespace.
func (*Provider) AccessPath(_ context.Context, key string) string {
	_, sub := splitRoutingKey(key)
	return attachmentpkg.MediaAccessPath(sub)
}

// OpenContainerFile opens a file from a bot's /data/ directory.
func (p *Provider) OpenContainerFile(ctx context.Context, botID, containerPath string) (io.ReadCloser, error) {
	subPath, ok := attachmentpkg.DataSubpath(containerPath)
	if !ok {
		if !filepath.IsAbs(strings.TrimSpace(containerPath)) {
			return nil, fmt.Errorf("path must start with %s/ or be an absolute workspace path", attachmentpkg.DataMountPath(""))
		}
		client, err := p.clients.MCPClient(ctx, botID)
		if err != nil {
			return nil, fmt.Errorf("get client: %w", err)
		}
		return client.ReadRaw(ctx, filepath.Clean(containerPath))
	}
	if subPath == "" || strings.Contains(subPath, "..") {
		return nil, errors.New("invalid workspace path")
	}
	client, err := p.clients.MCPClient(ctx, botID)
	if err != nil {
		return nil, fmt.Errorf("get client: %w", err)
	}
	return client.ReadRaw(ctx, subPath)
}

// ListPrefix returns all keys under the given routing prefix.
func (p *Provider) ListPrefix(ctx context.Context, prefix string) ([]string, error) {
	botID, sub := splitRoutingKey(prefix)
	if botID == "" || sub == "" {
		return nil, nil
	}
	client, err := p.clients.MCPClient(ctx, botID)
	if err != nil {
		return nil, nil
	}
	dir := filepath.Dir(filepath.Join(containerMediaRoot, sub))
	base := filepath.Base(sub)
	entries, err := client.ListDirAll(ctx, dir, false)
	if err != nil {
		return nil, nil
	}
	var keys []string
	for _, e := range entries {
		if e.GetIsDir() {
			continue
		}
		name := e.GetPath()
		if strings.HasPrefix(name, base) {
			storageKey := filepath.Join(filepath.Dir(sub), name)
			keys = append(keys, filepath.Join(botID, storageKey))
		}
	}
	return keys, nil
}

func parseRoutingKey(key string) (botID, storageKey string, err error) {
	clean := filepath.Clean(key)
	if filepath.IsAbs(clean) {
		return "", "", fmt.Errorf("absolute key is forbidden: %s", key)
	}
	if strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", "", fmt.Errorf("path traversal is forbidden: %s", key)
	}
	botID, sub := splitRoutingKey(clean)
	if strings.TrimSpace(botID) == "" || strings.TrimSpace(sub) == "" {
		return "", "", fmt.Errorf("invalid storage key: %s", key)
	}
	return botID, sub, nil
}

func splitRoutingKey(key string) (botID, storageKey string) {
	idx := strings.IndexByte(key, filepath.Separator)
	if idx <= 0 {
		return "", key
	}
	return key[:idx], key[idx+1:]
}
