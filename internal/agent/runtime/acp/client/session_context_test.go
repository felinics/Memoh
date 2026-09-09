package client

import (
	"strings"
	"testing"
)

func TestResolveSessionContextRejectsUnknownBackend(t *testing.T) {
	_, err := ResolveSessionContext(SessionContextInput{
		AgentID:   "acp",
		SetupMode: SetupModeAPIKey,
		Backend:   "remote",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported workspace backend") {
		t.Fatalf("ResolveSessionContext() error = %v, want unsupported backend", err)
	}
}
