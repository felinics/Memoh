package client

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
)

const claudeCodeAgentID = "claude-code"

func isClaudeCodeAgent(agentID string) bool {
	return strings.EqualFold(strings.TrimSpace(agentID), claudeCodeAgentID)
}

// claudeManagedSettings is the managed settings.json for Claude Code
// sessions. The explicit "ask" rule outranks the CLI's built-in auto-allow
// for "safe" read-only commands (pwd, ls, ...), so every Bash invocation
// reaches the permission prompt and therefore Memoh's tool approval - Memoh
// policy stays the single authority over what runs unasked.
var claudeManagedSettings = []byte(`{
  "permissions": {
    "ask": [
      "Bash"
    ]
  }
}
`)

// WriteClaudeManagedSettings writes the managed Claude Code settings into the
// given HOME directory (HOME/.claude/settings.json). Used for container
// sessions, where the managed HOME is fresh and CLAUDE_CONFIG_DIR is unset.
func WriteClaudeManagedSettings(ctx context.Context, client *bridge.Client, homeDir string) error {
	return writeClaudeSettingsDir(ctx, client, path.Join(homeDir, ".claude"))
}

func writeClaudeSettingsDir(ctx context.Context, client *bridge.Client, dir string) error {
	if client == nil {
		return errors.New("workspace bridge client is required")
	}
	if err := client.Mkdir(ctx, dir); err != nil {
		return fmt.Errorf("create Claude settings dir: %w", err)
	}
	if err := client.WriteFile(ctx, path.Join(dir, "settings.json"), claudeManagedSettings); err != nil {
		return fmt.Errorf("write Claude settings: %w", err)
	}
	return nil
}
