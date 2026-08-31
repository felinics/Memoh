// Package codex implements the direct codex app-server runtime driver: it
// speaks the v2 protocol (internal/agent/runtime/codex/protocol) to a pinned
// codex CLI running inside the bot workspace, with no ACP adapter in between.
//
// Session state is owned by codex itself under CODEX_HOME on the bot's
// persistent data volume; Memoh keeps only the thread id in session runtime
// metadata and projects the turn transcript into its own history.
package codex

import (
	"path"

	"github.com/felinics/memoh/internal/agent/runtime/codex/codexcfg"
	"github.com/felinics/memoh/internal/runtimekind"
)

const (
	// RuntimeType is the thread runtime type this driver serves.
	RuntimeType = string(runtimekind.Codex)

	// metadataThreadIDKey stores the codex thread id in session runtime
	// metadata. Losing it starts a fresh codex thread on the next turn.
	metadataThreadIDKey = "codex_thread_id"

	codexHomeRoot = "/data/.codex/agents"
	// launcherPath is the pinned toolkit launcher for the codex CLI.
	launcherPath = "/opt/memoh/toolkit/bin/codex"
	// defaultProjectPath matches the workspace data volume root.
	defaultProjectPath = "/data"
)

func codexHome(botAgentID string) string {
	return path.Join(codexHomeRoot, botAgentID)
}

// Configuration lives in the codexcfg leaf package so validators can import
// it without the driver's dependency tree; these aliases keep driver-side
// call sites short.
type (
	AuthMode = codexcfg.AuthMode
	Config   = codexcfg.Config
)

const (
	AuthAPIKey  = codexcfg.AuthAPIKey
	AuthChatGPT = codexcfg.AuthChatGPT
)

var (
	ErrNotConfigured = codexcfg.ErrNotConfigured
	ParseAgentConfig = codexcfg.ParseAgentConfig
)

func metadataString(meta map[string]any, key string) string {
	value, _ := meta[key].(string)
	return value
}
