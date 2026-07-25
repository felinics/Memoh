package sessionmode

import (
	"testing"
)

func TestSessionModeConstants(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"chat":      Chat,
		"heartbeat": Heartbeat,
		"schedule":  Schedule,
		"subagent":  Subagent,
		"discuss":   Discuss,
		"acp_agent": ACPAgent,
	}
	for want, got := range cases {
		if got != want {
			t.Fatalf("mode = %q, want %q", got, want)
		}
	}
}

func TestIsInteractive(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"", Chat, "CHAT", ACPAgent} {
		if !IsInteractive(mode) {
			t.Fatalf("expected %q to be interactive", mode)
		}
	}

	for _, mode := range []string{Discuss, Schedule, Heartbeat, Subagent} {
		if IsInteractive(mode) {
			t.Fatalf("expected %q to be non-interactive", mode)
		}
	}
}
