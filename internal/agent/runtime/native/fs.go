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

// ReadText reads a text file from the container, returning its content as a
// string. A missing file is bridge.ErrNotFound; without a provider every file
// is empty.
func (f *FSClient) ReadText(ctx context.Context, path string) (string, error) {
	if f.provider == nil {
		return "", nil
	}
	client, err := f.provider.MCPClient(ctx, f.botID)
	if err != nil {
		return "", fmt.Errorf("mcp client: %w", err)
	}
	resp, err := client.ReadFile(ctx, path, 0, 0)
	if err != nil {
		return "", err
	}
	return resp.GetContent(), nil
}

// LoadSystemFiles loads the standard set of system files from the bot container.
// A missing file is empty. A file that cannot be read is marked
// SystemFileUnavailable instead of passing for an empty one. Once the
// workspace is found unreachable the remaining files are not read.
func (f *FSClient) LoadSystemFiles(ctx context.Context) []SystemFile {
	home := "/data"
	filenames := []string{
		"AGENTS.md",
		"MEMORY.md",
		"PROFILES.md",
	}

	files := make([]SystemFile, len(filenames))
	for i, name := range filenames {
		files[i] = SystemFile{Filename: name}
		if bridge.WorkspaceUnavailableFromContext(ctx) {
			files[i].LoadStatus = SystemFileUnavailable
			continue
		}
		content, err := f.ReadText(ctx, home+"/"+name)
		switch {
		case err == nil:
			files[i].Content = strings.TrimSpace(content)
		case errors.Is(err, bridge.ErrNotFound):
		default:
			bridge.MarkWorkspaceUnavailableOn(ctx, err)
			files[i].LoadStatus = SystemFileUnavailable
		}
	}
	return files
}
