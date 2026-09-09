// Package runtimekind is the single owner of the session runtime vocabulary
// and of the capability table that says what each runtime kind supports.
//
// "Which runtime executes this session" is a cross-cutting concern: SQL
// constraints, message normalization, schedules, channel commands, discuss,
// forks, deletion, permissions, backups, and recovery all branch on it.
// Consumers share this table instead of copying the vocabulary and rules.
//
// This is deliberately a leaf package (stdlib-only) so that packages which
// cannot import the session domain (chat/message, schedule) still share the
// vocabulary instead of copying it.
package runtimekind

import "strings"

// Kind names one session runtime: the engine that executes a session's turns.
type Kind string

const (
	// Model is the built-in native model runtime (Twilight AI loop).
	Model Kind = "model"
	// ACPAgent is the ACP pool serving user-supplied custom agents.
	ACPAgent Kind = "acp_agent"
	// Codex is the direct codex external agent runtime.
	Codex Kind = "codex"
	// ClaudeCode is the direct Claude Code external agent runtime.
	ClaudeCode Kind = "claude-code"
)

// Session modes a runtime can host. The string values are the session_mode
// column vocabulary owned by chat/thread; the cross-package pin test keeps
// them equal.
const (
	ModeChat     = "chat"
	ModeDiscuss  = "discuss"
	ModeSchedule = "schedule"
	ModeSubagent = "subagent"
)

// capabilities declares what one runtime kind supports and requires. One row
// per kind, nothing derived at call sites: a consumer that needs a new
// distinction adds a field here so every runtime declares a value for it.
type capabilities struct {
	// External: turns dispatch through the external.Driver port instead of
	// the in-process model loop.
	External bool
	// Direct: a direct external agent runtime — external minus the ACP pool.
	// Direct kinds double as the agent's provider identity (the runtime name
	// IS the agent id in commands, settings, and BotAgent rows).
	Direct bool
	// DecisionWaiter: tool approvals and user inputs are answered by waking
	// an in-process waiter blocked inside the running turn. The model
	// runtime instead parks and re-enters the native loop with a tool
	// result.
	DecisionWaiter bool
	// WorkspaceExec: sessions execute inside the bot workspace, so the
	// runtime owner — and any actor driving the session — must hold the
	// workspace_exec permission.
	WorkspaceExec bool
	// AgentModel: the agent resolves its own model. Schedules and sessions
	// carry the agent-side model id (acp_model_id) instead of a Memoh model
	// row, and reasoning efforts use the agent's own vocabulary.
	AgentModel bool
	// SessionModes this runtime can host.
	SessionModes []string
}

// capabilityByKind is the one table.
var capabilityByKind = map[Kind]capabilities{
	Model: {
		SessionModes: []string{ModeChat, ModeDiscuss, ModeSchedule, ModeSubagent},
	},
	ACPAgent: {
		External:       true,
		DecisionWaiter: true,
		WorkspaceExec:  true,
		AgentModel:     true,
		SessionModes:   []string{ModeChat, ModeDiscuss, ModeSchedule},
	},
	Codex: {
		External:       true,
		Direct:         true,
		DecisionWaiter: true,
		WorkspaceExec:  true,
		AgentModel:     true,
		SessionModes:   []string{ModeChat, ModeDiscuss, ModeSchedule},
	},
	ClaudeCode: {
		External:       true,
		Direct:         true,
		DecisionWaiter: true,
		WorkspaceExec:  true,
		AgentModel:     true,
		SessionModes:   []string{ModeChat, ModeDiscuss, ModeSchedule},
	},
}

func capabilitiesFor(raw string) (capabilities, bool) {
	kind, ok := Normalize(raw)
	if !ok {
		return capabilities{}, false
	}
	return capabilityByKind[kind], true
}

// Normalize maps a raw runtime type string onto its Kind. It trims space and
// rejects unknown vocabulary; the empty string is unknown (legacy-row
// derivation is the session domain's job, not vocabulary's).
func Normalize(raw string) (Kind, bool) {
	switch Kind(strings.TrimSpace(raw)) {
	case Model:
		return Model, true
	case ACPAgent:
		return ACPAgent, true
	case Codex:
		return Codex, true
	case ClaudeCode:
		return ClaudeCode, true
	default:
		return "", false
	}
}

// Valid reports whether raw names a known runtime kind.
func Valid(raw string) bool {
	_, ok := Normalize(raw)
	return ok
}

// IsDirect reports a direct external agent runtime (codex, claude-code).
func IsDirect(raw string) bool {
	caps, ok := capabilitiesFor(raw)
	return ok && caps.Direct
}

// IsExternal reports any runtime dispatched through the external.Driver
// port (the ACP pool and the direct runtimes).
func IsExternal(raw string) bool {
	caps, ok := capabilitiesFor(raw)
	return ok && caps.External
}

// UsesDecisionWaiter reports a runtime whose decisions are consumed by an
// in-process waiter instead of a native-loop re-entry.
func UsesDecisionWaiter(raw string) bool {
	caps, ok := capabilitiesFor(raw)
	return ok && caps.DecisionWaiter
}

// RequiresWorkspaceExec reports a runtime whose sessions demand the
// workspace_exec permission from their runtime owner and driving actor.
func RequiresWorkspaceExec(raw string) bool {
	caps, ok := capabilitiesFor(raw)
	return ok && caps.WorkspaceExec
}

// ResolvesOwnModel reports a runtime whose model selection lives inside the
// agent rather than in Memoh model rows.
func ResolvesOwnModel(raw string) bool {
	caps, ok := capabilitiesFor(raw)
	return ok && caps.AgentModel
}

// SupportsSessionMode reports whether the runtime can host the session mode.
func SupportsSessionMode(raw, mode string) bool {
	caps, ok := capabilitiesFor(raw)
	if !ok {
		return false
	}
	mode = strings.TrimSpace(mode)
	for _, supported := range caps.SessionModes {
		if supported == mode {
			return true
		}
	}
	return false
}

// SupportedSessionModes lists the modes accepted by a runtime kind.
func SupportedSessionModes(raw string) []string {
	caps, ok := capabilitiesFor(raw)
	if !ok {
		return nil
	}
	return caps.SessionModes
}
