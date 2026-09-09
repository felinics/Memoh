package acpprofile

import "testing"

func TestCatalogExposesOnlyChannelSafeProfileData(t *testing.T) {
	catalog := NewCatalog()

	profile := catalog.ResolveACPProfile(" ACP ")
	if !profile.Known || profile.ID != "acp" || profile.DisplayName != "ACP" {
		t.Fatalf("profile = %#v, want normalized public generic identity", profile)
	}
	if unknown := catalog.ResolveACPProfile("missing"); unknown.Known || unknown.ID != "missing" {
		t.Fatalf("unknown profile = %#v, want normalized unknown identity", unknown)
	}
}

func TestCatalogPreflightDoesNotExposeManagedValues(t *testing.T) {
	catalog := NewCatalog()
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
	result := catalog.ResolveACPSetupPreflight("acp", metadata)

	if !result.Enabled {
		t.Fatal("preflight should preserve enabled state")
	}
	if result.MissingManagedField == nil ||
		result.MissingManagedField.ID != "command" ||
		result.MissingManagedField.Label != "Command" {
		t.Fatalf("missing field = %#v, want public command descriptor", result.MissingManagedField)
	}

	threadValidation := catalog.ValidateACPSetup("acp", metadata)
	if !threadValidation.Known || !threadValidation.Enabled || threadValidation.MissingManagedFieldID != "command" {
		t.Fatalf("thread validation = %#v, want known enabled agent missing command", threadValidation)
	}
	if unknown := catalog.ValidateACPSetup("missing", metadata); unknown.Known {
		t.Fatalf("unknown thread validation = %#v, want unknown agent", unknown)
	}
	// Former ACP providers are disowned: their sessions run direct runtimes.
	if moved := catalog.ValidateACPSetup("codex", metadata); moved.Known {
		t.Fatalf("codex validation = %#v, want unknown (direct runtime)", moved)
	}
	if moved := catalog.ValidateACPSetup("claude-code", metadata); moved.Known {
		t.Fatalf("claude-code validation = %#v, want unknown (direct runtime)", moved)
	}
}
