package sessionmode

import "strings"

const (
	Chat      = "chat"
	Heartbeat = "heartbeat"
	Schedule  = "schedule"
	Subagent  = "subagent"
	Discuss   = "discuss"
	ACPAgent  = "acp_agent"
)

// IsInteractive reports whether a run mode can pause and wait for user-facing
// approval or input. Discuss streams events to observers, but it does not have
// a chat-flow continuation path for deferred user input.
func IsInteractive(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", Chat, ACPAgent:
		return true
	default:
		return false
	}
}
