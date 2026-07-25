package profile

import "testing"

func TestListIncludesClaudeCode(t *testing.T) {
	items := List()
	if len(items) < 2 {
		t.Fatalf("profiles len = %d, want at least 2", len(items))
	}
	profile, ok := Lookup(AgentClaudeCodeID)
	if !ok {
		t.Fatalf("Claude Code profile was not registered")
	}
	if profile.Command != "claude-agent-acp" {
		t.Fatalf("Claude Code command = %q", profile.Command)
	}
	if profile.DynamicCommand != "npx" || len(profile.DynamicArgs) != 1 || profile.DynamicArgs[0] != "-y" || profile.DynamicPackage != "@agentclientprotocol/claude-agent-acp" {
		t.Fatalf("Claude Code dynamic launcher = %q %#v package %q", profile.DynamicCommand, profile.DynamicArgs, profile.DynamicPackage)
	}
	if len(profile.ManagedFields) == 0 || !profile.ManagedFields[0].Required {
		t.Fatalf("Claude Code profile should expose required API key field: %#v", profile.ManagedFields)
	}
	if len(profile.SetupModes) != 3 || profile.SetupModes[0] != setupModeAPIKey || profile.SetupModes[1] != setupModeOAuth || profile.SetupModes[2] != setupModeSelf {
		t.Fatalf("Claude Code setup modes = %#v", profile.SetupModes)
	}
	if profile.ReasoningConfigID != "effort" || profile.DefaultReasoningEffort != "high" {
		t.Fatalf("Claude Code reasoning mapping = %q / %q", profile.ReasoningConfigID, profile.DefaultReasoningEffort)
	}
	codex, ok := Lookup(AgentCodexID)
	if !ok || codex.DefaultReasoningEffort != "medium" || codex.ReasoningConfigID != "" {
		t.Fatalf("Codex reasoning profile = %#v", codex)
	}
}

func TestCodexUsesControlledACPAdapterUpgradeWithPinnedFallback(t *testing.T) {
	profile, ok := Lookup(AgentCodexID)
	if !ok {
		t.Fatal("Codex profile was not registered")
	}
	if profile.DynamicCommand != "npx" || len(profile.DynamicArgs) != 1 || profile.DynamicArgs[0] != "-y" || profile.DynamicPackage != "@agentclientprotocol/codex-acp" {
		t.Fatalf("Codex dynamic launcher = %q %#v package %q", profile.DynamicCommand, profile.DynamicArgs, profile.DynamicPackage)
	}
	if profile.Command != "codex-acp" {
		t.Fatalf("Codex fallback launcher = command %q", profile.Command)
	}
}

func TestListIncludesHermes(t *testing.T) {
	profile, ok := Lookup(AgentHermesID)
	if !ok {
		t.Fatalf("Hermes profile was not registered")
	}
	if profile.Command != "hermes-acp" {
		t.Fatalf("Hermes command = %q", profile.Command)
	}
	if len(profile.ManagedFields) != 4 {
		t.Fatalf("Hermes managed fields = %#v", profile.ManagedFields)
	}
	if len(profile.SetupModes) != 2 || profile.SetupModes[0] != setupModeSelf || profile.SetupModes[1] != setupModeAPIKey {
		t.Fatalf("Hermes setup modes = %#v", profile.SetupModes)
	}
	if len(profile.SupportedBackends) != 1 || profile.SupportedBackends[0] != "container" {
		t.Fatalf("Hermes supported backends = %#v", profile.SupportedBackends)
	}
	if !ShouldForceHTTPMCPServer(AgentHermesID) {
		t.Fatalf("Hermes should force HTTP MCP server injection until upstream advertises mcpCapabilities.http")
	}
	if ShouldForceHTTPMCPServer(AgentCodexID) {
		t.Fatalf("Codex should rely on advertised HTTP MCP capability")
	}
}

func TestMetadataAgentEnabled(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]any
		want     bool
	}{
		{
			name: "agent config enabled",
			metadata: map[string]any{
				MetadataKeyACP: map[string]any{
					"agents": map[string]any{
						AgentCodexID: map[string]any{"enabled": true},
					},
				},
			},
			want: true,
		},
		{
			name: "agent config disabled",
			metadata: map[string]any{
				MetadataKeyACP: map[string]any{
					"agents": map[string]any{
						AgentCodexID: map[string]any{"enabled": false},
					},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MetadataAgentEnabled(tt.metadata, AgentCodexID); got != tt.want {
				t.Fatalf("MetadataAgentEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseAgentSetupNormalizesLegacyManagedMode(t *testing.T) {
	apiKeySetup := ParseAgentSetup(map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				AgentHermesID: map[string]any{
					"enabled":    true,
					"setup_mode": "managed",
					"managed": map[string]any{
						"provider": "gemini",
						"model":    "gemini-3.5-flash",
						"api_key":  "AIza-test",
					},
				},
			},
		},
	}, AgentHermesID)
	if apiKeySetup.Mode != setupModeAPIKey {
		t.Fatalf("legacy managed mode = %q, want api_key", apiKeySetup.Mode)
	}

	oauthSetup := ParseAgentSetup(map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				AgentCodexID: map[string]any{
					"enabled":    true,
					"setup_mode": "managed",
					"managed": map[string]any{
						"auth_type": "provider_oauth",
					},
				},
			},
		},
	}, AgentCodexID)
	if oauthSetup.Mode != setupModeOAuth {
		t.Fatalf("legacy provider_oauth mode = %q, want oauth", oauthSetup.Mode)
	}
}

func TestMissingRequiredManagedFieldForPreflightSkipsLegacyMode(t *testing.T) {
	profile, ok := Lookup(AgentCodexID)
	if !ok {
		t.Fatal("Codex profile not registered")
	}
	setup := AgentSetup{
		AgentID: AgentCodexID,
		Enabled: true,
		Mode:    setupModeAPIKey,
		ModeSet: false,
		Managed: map[string]string{},
	}
	if field, missing := MissingRequiredManagedFieldForPreflight(profile, setup); missing {
		t.Fatalf("legacy preflight missing field = %#v, want none", field)
	}
	setup.ModeSet = true
	if field, missing := MissingRequiredManagedFieldForPreflight(profile, setup); !missing || field.ID != "api_key" {
		t.Fatalf("explicit api_key preflight = %#v, %v; want missing api_key", field, missing)
	}
}

func TestSensitiveMergeAndScrub(t *testing.T) {
	existing := map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				AgentCodexID: map[string]any{
					"enabled": true,
					"managed": map[string]any{
						"api_key":  "sk-oldsecret",
						"base_url": "https://example.test",
					},
				},
			},
		},
	}
	incoming := map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				AgentCodexID: map[string]any{
					"enabled": true,
					"managed": map[string]any{
						"api_key":  "sk-...cret",
						"base_url": "https://new.example",
					},
				},
			},
		},
	}

	merged := MergeSensitiveFieldsForUpdate(existing, incoming)
	setup := ParseAgentSetup(merged, AgentCodexID)
	if got := setup.Managed["api_key"]; got != "sk-oldsecret" {
		t.Fatalf("api_key = %q, want preserved old secret", got)
	}
	if got := setup.Managed["base_url"]; got != "https://new.example" {
		t.Fatalf("base_url = %q, want new value", got)
	}

	scrubbed := ScrubMetadataForResponse(merged)
	setup = ParseAgentSetup(scrubbed, AgentCodexID)
	if got := setup.Managed["api_key"]; got == "sk-oldsecret" || got == "" {
		t.Fatalf("scrubbed api_key = %q, want masked", got)
	}
}

func TestScrubMetadataForExportDropsManagedSecrets(t *testing.T) {
	metadata := map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				AgentHermesID: map[string]any{
					"enabled":    true,
					"setup_mode": "api_key",
					"managed": map[string]any{
						"provider": "openrouter",
						"model":    "anthropic/claude-sonnet-4",
						"api_key":  map[string]any{"value": "sk-hermes"},
					},
				},
				AgentCodexID: map[string]any{
					"enabled": true,
					"managed": map[string]any{
						"api_key":  "sk-codex",
						"base_url": "https://codex.example",
					},
				},
			},
		},
	}

	scrubbed, changed := ScrubMetadataForExport(metadata)
	if !changed {
		t.Fatal("ScrubMetadataForExport changed = false, want true")
	}
	hermesSetup := ParseAgentSetup(scrubbed, AgentHermesID)
	if _, ok := hermesSetup.Managed["api_key"]; ok {
		t.Fatalf("Hermes api_key was not removed: %#v", hermesSetup.Managed)
	}
	if hermesSetup.Managed["provider"] != "openrouter" || hermesSetup.Managed["model"] != "anthropic/claude-sonnet-4" {
		t.Fatalf("Hermes non-secret managed fields = %#v", hermesSetup.Managed)
	}
	codexSetup := ParseAgentSetup(scrubbed, AgentCodexID)
	if _, ok := codexSetup.Managed["api_key"]; ok {
		t.Fatalf("Codex api_key was not removed: %#v", codexSetup.Managed)
	}
	if codexSetup.Managed["base_url"] != "https://codex.example" {
		t.Fatalf("Codex base_url = %q", codexSetup.Managed["base_url"])
	}

	acpConfig, _ := metadataRecord(metadata[MetadataKeyACP])
	agents, _ := metadataRecord(acpConfig["agents"])
	agentConfig, _ := metadataRecord(agents[AgentHermesID])
	managed, _ := metadataRecord(agentConfig["managed"])
	if _, ok := managed["api_key"].(map[string]any); !ok {
		t.Fatalf("original metadata mutated: %#v", managed)
	}
}

func TestMergeSensitiveFieldsThreeState(t *testing.T) {
	existing := map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				AgentCodexID: map[string]any{
					"enabled": true,
					"managed": map[string]any{
						"api_key":  "sk-existing",
						"base_url": "https://old.example",
					},
				},
			},
		},
	}

	preserve := MergeSensitiveFieldsForUpdate(existing, map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				AgentCodexID: map[string]any{
					"enabled": true,
					"managed": map[string]any{
						"base_url": "https://new.example",
					},
				},
			},
		},
	})
	if got := ParseAgentSetup(preserve, AgentCodexID).Managed["api_key"]; got != "sk-existing" {
		t.Fatalf("missing api_key update preserved %q, want existing secret", got)
	}

	cleared := MergeSensitiveFieldsForUpdate(existing, map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				AgentCodexID: map[string]any{
					"enabled": true,
					"managed": map[string]any{
						"api_key": nil,
					},
				},
			},
		},
	})
	if _, ok := ParseAgentSetup(cleared, AgentCodexID).Managed["api_key"]; ok {
		t.Fatalf("nil api_key update should clear existing secret")
	}

	overwritten := MergeSensitiveFieldsForUpdate(existing, map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				AgentCodexID: map[string]any{
					"enabled": true,
					"managed": map[string]any{
						"api_key": "sk-new",
					},
				},
			},
		},
	})
	if got := ParseAgentSetup(overwritten, AgentCodexID).Managed["api_key"]; got != "sk-new" {
		t.Fatalf("new api_key update = %q, want overwrite", got)
	}

	dottedSecret := MergeSensitiveFieldsForUpdate(existing, map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				AgentCodexID: map[string]any{
					"enabled": true,
					"managed": map[string]any{
						"api_key": "https://acme.example.com/v1/...",
					},
				},
			},
		},
	})
	if got := ParseAgentSetup(dottedSecret, AgentCodexID).Managed["api_key"]; got != "https://acme.example.com/v1/..." {
		t.Fatalf("dotted api_key update = %q, want literal value", got)
	}
}
