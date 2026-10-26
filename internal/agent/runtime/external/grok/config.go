// Package grok implements the direct Grok Build runtime driver. Grok Build
// speaks ACP natively (`grok agent stdio`), so the wire protocol is the shared
// ACP client; this package owns what is Grok's own: the launcher, the
// credential, the state directory, and its permission modes.
//
// One process serves every conversation of a Bot Agent. Grok writes its
// sessions under GROK_HOME on the bot's data volume; they are the only copy
// of its conversation. Memoh keeps the session id in thread metadata and
// resumes from it; a session Grok can no longer resume starts over and says
// so.
//
// An Agent authenticates with an xAI API key or a Grok account. The account
// sign-in runs inside the Agent's own process (see login.go); Grok writes the
// login file into the Agent's home on the bot's data volume and keeps and
// refreshes it there. Memoh stores no copy.
package grok

import (
	"path"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/external/grok/grokcfg"
	"github.com/felinics/memoh/internal/runtimekind"
	"github.com/felinics/memoh/internal/workspace/vpath"
)

const (
	// RuntimeType is the thread runtime type this driver serves.
	RuntimeType = string(runtimekind.GrokBuild)

	// metadataSessionIDKey stores the Grok session id in session runtime
	// metadata.
	metadataSessionIDKey = "grok_session_id"
	// metadataLastPromptIDKey stores the id Grok gave the conversation's
	// latest prompt, which is where a fork is possible (see ForkThread).
	metadataLastPromptIDKey = "grok_last_prompt_id"

	// pinnedVersion is the Grok Build release whose extensions below were
	// checked; a process of another version is logged at start.
	pinnedVersion = "1.0.62"

	// homeRoot is per Agent so one Agent's credential never meets a login
	// another Agent or a workspace terminal left behind: Grok prefers a
	// stored login over XAI_API_KEY.
	homeRoot = "/data/.grok/agents"
	// dependencyID names the managed workspace dependency that provides the
	// grok CLI; the catalog entry must use the same id.
	dependencyID = "grok"
	// defaultLauncherPath is used only when no LauncherResolver is installed.
	defaultLauncherPath = "/opt/memoh/toolkit/bin/grok"
	defaultProjectPath  = vpath.DataMount

	// containerPath puts the managed dependency shims first so a managed
	// runtime overlays the toolkit copy.
	containerPath = vpath.DataMount + "/.memoh/deps/bin:/opt/memoh/toolkit/bin:/usr/local/bin:/usr/bin:/bin"
)

// Grok's own ACP extension methods, with the shapes Grok 1.0.62 answers.
const (
	// {session_id} compacts the session; nothing is returned.
	compactMethod = "_x.ai/compact_conversation"
	// {sourceSessionId, sourceCwd, newCwd} copies the whole session and
	// answers {newSessionId}. Grok has no cut point.
	forkMethod = "_x.ai/session/fork"
	// {} answers {result: {currentModelId, availableModels}}, the catalog as
	// Grok reads it now; the initialize _meta carries the snapshot of start.
	modelsMethod = "_x.ai/models/list"
	// modelsUpdateMethod is the notification that carries the same object
	// when the catalog changes.
	modelsUpdateMethod = "_x.ai/models/update"
	// {sessionId} answers {result: {model, context: {used, total, ...}}} for
	// a session the process holds open, and an empty result for any other.
	sessionInfoMethod = "_x.ai/session/info"
	// mcpStatusMethod is the notification {sessionId, name, status, reason,
	// detail} Grok sends when an MCP server of a session connects or fails,
	// within a moment of the session opening.
	mcpStatusMethod = "_x.ai/mcp/server_status"
	// mcpStatusUnavailable is the status of a server Grok could not use.
	mcpStatusUnavailable = "unavailable"
)

func grokHome(botAgentID string) string {
	return path.Join(homeRoot, botAgentID)
}

// Configuration lives in the grokcfg leaf package so validators can import
// it without the driver's dependency tree.
type Config = grokcfg.Config

// sessionMeta is the _meta Grok reads on session/new and session/resume.
// Grok does not store the permission mode with the session, and an omitted
// flag falls back to whatever its config file says, so both flags are always
// stated.
func sessionMeta(mode string) map[string]any {
	return map[string]any{
		"yoloMode": mode == grokcfg.PermissionAlwaysApprove,
		"autoMode": mode == grokcfg.PermissionAuto,
	}
}

// mcpToolCall reads the target of a use_tool call. Grok reaches every MCP
// tool through that one meta-tool and names the target `server__tool`.
func mcpToolCall(rawInput any) (serverName, toolName string, ok bool) {
	fields, _ := rawInput.(map[string]any)
	qualified, _ := fields["tool_name"].(string)
	serverName, toolName, ok = strings.Cut(qualified, "__")
	if !ok || serverName == "" || toolName == "" {
		return "", "", false
	}
	return serverName, toolName, true
}

func permissionMode(metadata map[string]any, cfg Config) string {
	return firstNonEmpty(metadataString(metadata, "permission_mode"), cfg.PermissionMode, grokcfg.PermissionAsk)
}

func metadataString(meta map[string]any, key string) string {
	value, _ := meta[key].(string)
	return value
}
