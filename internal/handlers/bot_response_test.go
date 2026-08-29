package handlers

import (
	"testing"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/bots"
)

func TestScrubBotForResponseMasksACPManagedSecrets(t *testing.T) {
	original := bots.Bot{
		ID: "bot-1",
		Metadata: map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{
						"enabled": true,
						"managed": map[string]any{
							"api_key":  "sk-original-secret",
							"base_url": "https://api.example.test/v1",
						},
					},
				},
			},
		},
	}

	resp := scrubBotForResponse(original)
	setup := acpprofile.ParseAgentSetup(resp.Metadata, acpprofile.AgentCodexID)
	if got := setup.Managed["api_key"]; got == "" || got == "sk-original-secret" {
		t.Fatalf("scrubbed api_key = %q, want masked non-empty value", got)
	}
	if got := setup.Managed["base_url"]; got != "https://api.example.test/v1" {
		t.Fatalf("base_url = %q, want non-sensitive value preserved", got)
	}

	originalSetup := acpprofile.ParseAgentSetup(original.Metadata, acpprofile.AgentCodexID)
	if got := originalSetup.Managed["api_key"]; got != "sk-original-secret" {
		t.Fatalf("original api_key = %q, want original metadata left untouched", got)
	}
}

func TestScrubBotsForResponseScrubsEachItem(t *testing.T) {
	items := []bots.Bot{
		{
			ID: "bot-1",
			Metadata: map[string]any{
				acpprofile.MetadataKeyACP: map[string]any{
					"agents": map[string]any{
						acpprofile.AgentCodexID: map[string]any{
							"managed": map[string]any{"api_key": "sk-one-secret"},
						},
					},
				},
			},
		},
		{
			ID: "bot-2",
			Metadata: map[string]any{
				acpprofile.MetadataKeyACP: map[string]any{
					"agents": map[string]any{
						acpprofile.AgentCodexID: map[string]any{
							"managed": map[string]any{"api_key": "sk-two-secret"},
						},
					},
				},
			},
		},
	}

	resp := scrubBotsForResponse(items)
	if len(resp) != len(items) {
		t.Fatalf("response len = %d, want %d", len(resp), len(items))
	}
	for _, item := range resp {
		setup := acpprofile.ParseAgentSetup(item.Metadata, acpprofile.AgentCodexID)
		if got := setup.Managed["api_key"]; got == "" || got == "sk-one-secret" || got == "sk-two-secret" {
			t.Fatalf("bot %s api_key = %q, want masked", item.ID, got)
		}
	}
}

func TestScrubBotForResponseRemovesWorkspaceSetupError(t *testing.T) {
	original := bots.Bot{
		ID: "bot-1",
		Metadata: map[string]any{
			"workspace": map[string]any{
				"backend":               "container",
				"image":                 "ghcr.io/felinics/workspace:latest",
				"last_setup_error":      map[string]any{"message": "pull failed"},
				"skill_discovery_roots": []any{"/data/skills"},
			},
		},
	}

	resp := scrubBotForResponse(original)
	workspace, ok := resp.Metadata["workspace"].(map[string]any)
	if !ok {
		t.Fatalf("workspace metadata missing: %#v", resp.Metadata)
	}
	if _, ok := workspace["last_setup_error"]; ok {
		t.Fatalf("last_setup_error should be removed from response: %#v", workspace)
	}
	if got := workspace["backend"]; got != "container" {
		t.Fatalf("workspace backend = %#v, want container", got)
	}
	if got := workspace["image"]; got != "ghcr.io/felinics/workspace:latest" {
		t.Fatalf("workspace image = %#v, want preserved image", got)
	}

	originalWorkspace := original.Metadata["workspace"].(map[string]any)
	if _, ok := originalWorkspace["last_setup_error"]; !ok {
		t.Fatalf("original metadata should be left untouched: %#v", originalWorkspace)
	}
}

func TestScrubBotChecksForResponseDropsDetailsForNonManage(t *testing.T) {
	items := []bots.BotCheck{
		{
			ID:      bots.BotCheckTypeContainerInit,
			Status:  bots.BotCheckStatusError,
			Summary: "Workspace initialization failed.",
			Detail:  "registry token leaked",
			Metadata: map[string]any{
				"setup_error_phase": "setup",
			},
		},
	}

	resp := scrubBotChecksForResponse(items, false)
	if len(resp) != 1 {
		t.Fatalf("response len = %d, want 1", len(resp))
	}
	if resp[0].Detail != "" {
		t.Fatalf("detail = %q, want empty", resp[0].Detail)
	}
	if resp[0].Metadata != nil {
		t.Fatalf("metadata = %#v, want nil", resp[0].Metadata)
	}
	if resp[0].Summary != items[0].Summary || resp[0].Status != items[0].Status {
		t.Fatalf("status/summary should be preserved: %#v", resp[0])
	}

	manageResp := scrubBotChecksForResponse(items, true)
	if manageResp[0].Detail != items[0].Detail || manageResp[0].Metadata == nil {
		t.Fatalf("manage response should preserve detail/metadata: %#v", manageResp[0])
	}
}
