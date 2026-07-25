package client

import (
	"strings"
	"testing"
)

func TestResolveSessionContextRejectsUnknownBackend(t *testing.T) {
	_, err := ResolveSessionContext(SessionContextInput{
		AgentID:   "hermes",
		SetupMode: SetupModeAPIKey,
		Backend:   "remote",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported workspace backend") {
		t.Fatalf("ResolveSessionContext() error = %v, want unsupported backend", err)
	}
}

func TestResolveSessionContextHermesManagedHome(t *testing.T) {
	resolved, err := ResolveSessionContext(SessionContextInput{
		AgentID:     "hermes",
		SetupMode:   SetupModeAPIKey,
		Backend:     "container",
		ProjectPath: "/data/project",
	})
	if err != nil {
		t.Fatalf("ResolveSessionContext() error = %v", err)
	}
	if resolved.HermesHome != HermesContainerHome {
		t.Fatalf("HermesHome = %q, want %q", resolved.HermesHome, HermesContainerHome)
	}
	if resolved.CWD != "/data/project" {
		t.Fatalf("CWD = %q, want /data/project", resolved.CWD)
	}
}

func TestResolveSessionContextHermesSelfDoesNotSetManagedHome(t *testing.T) {
	resolved, err := ResolveSessionContext(SessionContextInput{
		AgentID:     "hermes",
		SetupMode:   SetupModeSelf,
		Backend:     "container",
		ProjectPath: "/data",
	})
	if err != nil {
		t.Fatalf("ResolveSessionContext() error = %v", err)
	}
	if resolved.HermesHome != "" {
		t.Fatalf("HermesHome = %q, want empty", resolved.HermesHome)
	}
}
