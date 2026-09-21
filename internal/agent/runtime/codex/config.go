// Package codex implements the direct codex app-server runtime driver: it
// speaks the v2 protocol (internal/agent/runtime/codex/protocol) to a pinned
// codex CLI running inside the bot workspace, with no ACP adapter in between.
//
// Codex writes native rollouts under CODEX_HOME. Memoh checkpoints their
// ordered JSONL records in the session store and publishes the checkpoint
// with the chat round. Workspace rollouts are a resumable local cache.
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
	// metadata. A published checkpoint takes precedence over this hint.
	metadataThreadIDKey           = "codex_thread_id"
	metadataCheckpointRequiredKey = "codex_checkpoint_required"

	codexHomeRoot = "/data/.codex/agents"
	// dependencyID names the managed workspace dependency that provides the
	// codex CLI; the catalog entry must use the same id.
	dependencyID = "codex"
	// defaultLauncherPath is the toolkit path of the codex CLI, used only when
	// no LauncherResolver is installed. The canonical workspace image does
	// not ship an agent CLI, so this only resolves in custom images that
	// provide one; with a resolver the copy to run is chosen per bot.
	defaultLauncherPath = "/opt/memoh/toolkit/bin/codex"
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
