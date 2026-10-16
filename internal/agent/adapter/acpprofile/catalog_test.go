package acpprofile

import (
	"context"
	"testing"

	runtimeprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
)

func TestCatalogExposesOnlyChannelSafeProfileData(t *testing.T) {
	catalog := NewCatalog(nil)

	profile := catalog.ResolveACPProfile(" ACP ")
	if !profile.Known || profile.ID != "acp" || profile.DisplayName != "ACP" {
		t.Fatalf("profile = %#v, want normalized public generic identity", profile)
	}
	if unknown := catalog.ResolveACPProfile("missing"); unknown.Known || unknown.ID != "missing" {
		t.Fatalf("unknown profile = %#v, want normalized unknown identity", unknown)
	}
}

func TestCatalogPreflightDoesNotExposeManagedValues(t *testing.T) {
	catalog := NewCatalog(nil)
	metadata := map[string]any{
		"acp": map[string]any{
			"agents": map[string]any{
				"acp": map[string]any{
					"enabled":    true,
					"setup_mode": "api_key",
					"managed":    map[string]any{},
				},
			},
		},
	}
	result, err := catalog.ResolveACPSetupPreflight(context.Background(), "bot-1", "", "acp", metadata)
	if err != nil {
		t.Fatalf("ResolveACPSetupPreflight() error = %v", err)
	}

	if !result.Enabled {
		t.Fatal("preflight should preserve enabled state")
	}
	if result.MissingManagedField == nil ||
		result.MissingManagedField.ID != "command" ||
		result.MissingManagedField.Label != "Command" {
		t.Fatalf("missing field = %#v, want public command descriptor", result.MissingManagedField)
	}

	threadValidation, err := catalog.ValidateACPSetup(context.Background(), "bot-1", "", "acp", metadata)
	if err != nil {
		t.Fatalf("ValidateACPSetup() error = %v", err)
	}
	if !threadValidation.Enabled || threadValidation.MissingManagedFieldID != "command" {
		t.Fatalf("thread validation = %#v, want enabled agent missing command", threadValidation)
	}
	if !catalog.KnownACPAgent("acp") || catalog.KnownACPAgent("missing") {
		t.Fatal("KnownACPAgent should accept only registered profiles")
	}
	// Former ACP providers are disowned: their sessions run direct runtimes.
	if catalog.KnownACPAgent("codex") || catalog.KnownACPAgent("claude-code") {
		t.Fatal("direct runtimes must not be known ACP agents")
	}
}

type recordingSetupResolver struct {
	botAgentID string
	setup      runtimeprofile.AgentSetup
}

func (r *recordingSetupResolver) ResolveACPSetup(_ context.Context, _, botAgentID, _ string, _ map[string]any) (runtimeprofile.AgentSetup, error) {
	r.botAgentID = botAgentID
	return r.setup, nil
}

func TestCatalogValidatesTheBoundInstanceSetup(t *testing.T) {
	resolver := &recordingSetupResolver{setup: runtimeprofile.AgentSetup{
		Enabled: true,
		Mode:    "api_key",
		ModeSet: true,
		Managed: map[string]string{"command": "grok-acp"},
	}}
	catalog := NewCatalog(resolver)

	// The bot's legacy slot is unconfigured; only the instance carries a command.
	validation, err := catalog.ValidateACPSetup(context.Background(), "bot-1", "agent-grok", "acp", map[string]any{})
	if err != nil {
		t.Fatalf("ValidateACPSetup() error = %v", err)
	}
	if resolver.botAgentID != "agent-grok" {
		t.Fatalf("resolved instance = %q, want the thread's bound instance", resolver.botAgentID)
	}
	if !validation.Enabled || validation.MissingManagedFieldID != "" {
		t.Fatalf("validation = %#v, want the instance's configured setup", validation)
	}
}
