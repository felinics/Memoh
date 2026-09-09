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

	attachmentpkg "github.com/felinics/memoh/internal/attachment"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	containerMediaRoot       = ".memoh/media"
	legacyContainerMediaRoot = "media"
)

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
	paths := mediaContainerPaths(sub)
	reader, err := client.ReadRaw(ctx, paths[0])
	if err == nil || !errors.Is(err, bridge.ErrNotFound) {
		return reader, err
	}
	return client.ReadRaw(ctx, paths[1])
}

// Delete removes both current and legacy copies during the transition.
func (p *Provider) Delete(ctx context.Context, key string) error {
	botID, sub, err := parseRoutingKey(key)
	if err != nil {
		return err
	}
	client, err := p.clients.MCPClient(ctx, botID)
	if err != nil {
		return fmt.Errorf("get client: %w", err)
	}
	var errs []error
	for _, containerPath := range mediaContainerPaths(sub) {
		if err := client.DeleteFile(ctx, containerPath, false); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// AccessPath returns a reachable current or legacy workspace path.
func (p *Provider) AccessPath(ctx context.Context, key string) string {
	botID, sub, err := parseRoutingKey(key)
	if err != nil {
		return ""
	}
	paths := mediaContainerPaths(sub)
	if p == nil || p.clients == nil {
		return attachmentpkg.DataMountPath(paths[0])
	}
	client, err := p.clients.MCPClient(ctx, botID)
	if err != nil {
		return ""
	}
	for _, containerPath := range paths {
		if _, err := client.Stat(ctx, containerPath); err == nil {
			return attachmentpkg.DataMountPath(containerPath)
		} else if !errors.Is(err, bridge.ErrNotFound) {
			return ""
		}
	}
	return ""
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
	// Read with the absolute container path, not the stripped subPath: workspace
	// clients resolve relative paths against different bases (the in-container
	// bridge joins its /data workdir, but the runtime-worker chain resolves
	// against the sandbox home), so a relative subPath reads the wrong file
	// anywhere off the bridge. Absolute paths resolve consistently everywhere.
	return client.ReadRaw(ctx, filepath.Clean(containerPath))
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
	base := filepath.Base(sub)
	var keys []string
	seen := make(map[string]struct{})
	for _, containerPath := range mediaContainerPaths(sub) {
		entries, err := client.ListDirAll(ctx, filepath.Dir(containerPath), false)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.GetIsDir() {
				continue
			}
			name := e.GetPath()
			if !strings.HasPrefix(name, base) {
				continue
			}
			key := filepath.Join(botID, filepath.Dir(sub), name)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func mediaContainerPaths(sub string) [2]string {
	return [2]string{
		filepath.Join(containerMediaRoot, sub),
		filepath.Join(legacyContainerMediaRoot, sub),
	}
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
