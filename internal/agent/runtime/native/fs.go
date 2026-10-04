package native

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

// FSClient provides file operations against a bot's container filesystem.
type FSClient struct {
	provider bridge.Provider
	botID    string
	now      func() time.Time
}

// NewFSClient creates a new container filesystem client.
func NewFSClient(provider bridge.Provider, botID string, now func() time.Time) *FSClient {
	if now == nil {
		now = time.Now
	}
	return &FSClient{provider: provider, botID: botID, now: now}
}

// ReadText reads a text file from the container, returning its content as a string.
// Missing files and unavailable workspaces return distinct errors.
func (f *FSClient) ReadText(ctx context.Context, path string) (string, error) {
	if bridge.WorkspaceUnavailableFromContext(ctx) {
		return "", bridge.ErrUnavailable
	}
	if f.provider == nil {
		return "", bridge.ErrUnavailable
	}
	client, err := f.provider.MCPClient(ctx, f.botID)
	if err != nil {
		return "", fmt.Errorf("mcp client: %w", err)
	}
	resp, err := client.ReadFile(ctx, path, 0, 0)
	if err != nil {
		return "", err
	}
	if resp.GetBinary() {
		return "", bridge.ErrBadRequest
	}
	return resp.GetContent(), nil
}

// ReadTextSafe reads a text file, returning empty string on any error.
func (f *FSClient) ReadTextSafe(ctx context.Context, path string) string {
	content, _ := f.ReadText(ctx, path)
	return content
}

// LoadSystemFiles loads the standard set of system files from the bot container.
func (f *FSClient) LoadSystemFiles(ctx context.Context) []SystemFile {
	ctx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
	defer cancel()
	home := "/data"
	filenames := []string{
		"AGENTS.md",
		"MEMORY.md",
		"PROFILES.md",
	}

	files := make([]SystemFile, len(filenames))
	for i, name := range filenames {
		content, err := f.ReadText(ctx, home+"/"+name)
		files[i] = SystemFile{
			Filename: name,
			Content:  strings.TrimSpace(content),
		}
		if errors.Is(err, bridge.ErrNotFound) {
			files[i].LoadStatus = "missing"
		} else if err != nil {
			files[i].LoadStatus = "unavailable"
		}
	}
	return files
}
