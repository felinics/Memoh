package profile

import "testing"

func TestCatalogExposesOnlyChannelSafeProfileData(t *testing.T) {
	catalog := NewCatalog()

	profile := catalog.ResolveACPProfile(" CODEX ")
	if !profile.Known || profile.ID != "codex" || profile.DisplayName != "Codex" {
		t.Fatalf("profile = %#v, want normalized public Codex identity", profile)
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
				"claude-code": map[string]any{
					"enabled":    true,
					"setup_mode": "api_key",
					"managed":    map[string]any{},
				},
			},
		},
	}
	result := catalog.ResolveACPSetupPreflight("claude-code", metadata)

	if !result.Enabled {
		t.Fatal("preflight should preserve enabled state")
	}
	if result.MissingManagedField == nil ||
		result.MissingManagedField.ID != "api_key" ||
		result.MissingManagedField.Label != "Anthropic API key" {
		t.Fatalf("missing field = %#v, want public api_key descriptor", result.MissingManagedField)
	}

	threadValidation := catalog.ValidateACPSetup("claude-code", metadata)
	if !threadValidation.Known || !threadValidation.Enabled || threadValidation.MissingManagedFieldID != "api_key" {
		t.Fatalf("thread validation = %#v, want known enabled agent missing api_key", threadValidation)
	}
	if unknown := catalog.ValidateACPSetup("missing", metadata); unknown.Known {
		t.Fatalf("unknown thread validation = %#v, want unknown agent", unknown)
	}
}
