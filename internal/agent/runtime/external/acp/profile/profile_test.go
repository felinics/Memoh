package profile

import "testing"

func TestListIncludesGenericACP(t *testing.T) {
	profile, ok := Lookup(AgentACPID)
	if !ok {
		t.Fatal("generic ACP profile was not registered")
	}
	if profile.Launch.ManagedCommandField != "command" || profile.Launch.ManagedArgumentsField != "arguments" {
		t.Fatalf("generic ACP launch policy = %#v, want managed command and arguments", profile.Launch)
	}
	if len(profile.ManagedFields) != 2 || profile.ManagedFields[0].ID != "command" || !profile.ManagedFields[0].Required || profile.ManagedFields[1].ID != "arguments" {
		t.Fatalf("generic ACP managed fields = %#v", profile.ManagedFields)
	}
	if len(profile.SetupModes) != 1 || profile.SetupModes[0] != setupModeAPIKey {
		t.Fatalf("generic ACP setup modes = %#v", profile.SetupModes)
	}
}

func TestResolveGenericACPLaunch(t *testing.T) {
	profile, ok := Lookup(AgentACPID)
	if !ok {
		t.Fatal("generic ACP profile was not registered")
	}
	command, arguments, err := ResolveLaunch(profile, AgentSetup{Managed: map[string]string{
		"command":   "  uvx  ",
		"arguments": "agent-package\r\n--mode\nvalue with spaces\n\n",
	}})
	if err != nil {
		t.Fatalf("ResolveLaunch() error = %v", err)
	}
	if command != "uvx" {
		t.Fatalf("ResolveLaunch() command = %q, want uvx", command)
	}
	want := []string{"agent-package", "--mode", "value with spaces"}
	if len(arguments) != len(want) {
		t.Fatalf("ResolveLaunch() arguments = %#v, want %#v", arguments, want)
	}
	for i := range want {
		if arguments[i] != want[i] {
			t.Fatalf("ResolveLaunch() arguments[%d] = %q, want %q", i, arguments[i], want[i])
		}
	}
}

func TestResolveGenericACPLaunchRejectsInvalidCommand(t *testing.T) {
	profile, ok := Lookup(AgentACPID)
	if !ok {
		t.Fatal("generic ACP profile was not registered")
	}
	if _, _, err := ResolveLaunch(profile, AgentSetup{Managed: map[string]string{"command": "uvx\nagent"}}); err == nil {
		t.Fatal("ResolveLaunch() error = nil, want invalid command error")
	}
}

func TestResolveLaunchUsesProfileManagedPolicyWithoutKnownAgentID(t *testing.T) {
	custom := Profile{
		ID:          "custom-agent",
		DisplayName: "Custom Agent",
		Launch: LaunchPolicy{
			ManagedCommandField:   "executable",
			ManagedArgumentsField: "argv",
		},
	}
	command, arguments, err := ResolveLaunch(custom, AgentSetup{Managed: map[string]string{
		"executable": "custom-acp",
		"argv":       "--stdio\n--verbose",
	}})
	if err != nil {
		t.Fatalf("ResolveLaunch() error = %v", err)
	}
	if command != "custom-acp" || len(arguments) != 2 || arguments[0] != "--stdio" || arguments[1] != "--verbose" {
		t.Fatalf("ResolveLaunch() = %q, %#v; want profile-managed launch", command, arguments)
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
						"codex": map[string]any{"enabled": true},
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
						"codex": map[string]any{"enabled": false},
					},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MetadataAgentEnabled(tt.metadata, "codex"); got != tt.want {
				t.Fatalf("MetadataAgentEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseAgentSetupNormalizesLegacyManagedMode(t *testing.T) {
	apiKeySetup := ParseAgentSetup(map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				"custom-agent": map[string]any{
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
	}, "custom-agent")
	if apiKeySetup.Mode != setupModeAPIKey {
		t.Fatalf("legacy managed mode = %q, want api_key", apiKeySetup.Mode)
	}

	oauthSetup := ParseAgentSetup(map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				"codex": map[string]any{
					"enabled":    true,
					"setup_mode": "managed",
					"managed": map[string]any{
						"auth_type": "provider_oauth",
					},
				},
			},
		},
	}, "codex")
	if oauthSetup.Mode != setupModeOAuth {
		t.Fatalf("legacy provider_oauth mode = %q, want oauth", oauthSetup.Mode)
	}
}

func TestMissingRequiredManagedFieldForPreflightRequiresCommand(t *testing.T) {
	profile, ok := Lookup(AgentACPID)
	if !ok {
		t.Fatal("generic ACP profile not registered")
	}
	setup := AgentSetup{
		AgentID: AgentACPID,
		Enabled: true,
		Mode:    setupModeAPIKey,
		ModeSet: true,
		Managed: map[string]string{},
	}
	if field, missing := MissingRequiredManagedFieldForPreflight(profile, setup); !missing || field.ID != "command" {
		t.Fatalf("preflight = %#v, %v; want missing command", field, missing)
	}
	setup.Managed["command"] = "my-agent-acp"
	if field, missing := MissingRequiredManagedFieldForPreflight(profile, setup); missing {
		t.Fatalf("preflight with command = %#v, want none", field)
	}
}

func TestMissingRequiredManagedFieldForPreflightRequiresGenericACPCommand(t *testing.T) {
	profile, ok := Lookup(AgentACPID)
	if !ok {
		t.Fatal("generic ACP profile not registered")
	}
	setup := AgentSetup{
		AgentID: AgentACPID,
		Enabled: true,
		Mode:    setupModeAPIKey,
		ModeSet: false,
		Managed: map[string]string{},
	}
	if field, missing := MissingRequiredManagedFieldForPreflight(profile, setup); !missing || field.ID != "command" {
		t.Fatalf("generic ACP preflight = %#v, %v; want missing command", field, missing)
	}
}

func TestSensitiveMergeAndScrub(t *testing.T) {
	existing := map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				"codex": map[string]any{
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
				"codex": map[string]any{
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
	setup := ParseAgentSetup(merged, "codex")
	if got := setup.Managed["api_key"]; got != "sk-oldsecret" {
		t.Fatalf("api_key = %q, want preserved old secret", got)
	}
	if got := setup.Managed["base_url"]; got != "https://new.example" {
		t.Fatalf("base_url = %q, want new value", got)
	}

	scrubbed := ScrubMetadataForResponse(merged)
	setup = ParseAgentSetup(scrubbed, "codex")
	if got := setup.Managed["api_key"]; got == "sk-oldsecret" || got == "" {
		t.Fatalf("scrubbed api_key = %q, want masked", got)
	}
}

func TestScrubMetadataForExportDropsManagedSecrets(t *testing.T) {
	metadata := map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				"custom-agent": map[string]any{
					"enabled":    true,
					"setup_mode": "api_key",
					"managed": map[string]any{
						"provider": "openrouter",
						"model":    "anthropic/claude-sonnet-4",
						"api_key":  map[string]any{"value": "custom-secret"},
					},
				},
				"codex": map[string]any{
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
	customSetup := ParseAgentSetup(scrubbed, "custom-agent")
	if _, ok := customSetup.Managed["api_key"]; ok {
		t.Fatalf("custom agent api_key was not removed: %#v", customSetup.Managed)
	}
	if customSetup.Managed["provider"] != "openrouter" || customSetup.Managed["model"] != "anthropic/claude-sonnet-4" {
		t.Fatalf("custom agent non-secret managed fields = %#v", customSetup.Managed)
	}
	codexSetup := ParseAgentSetup(scrubbed, "codex")
	if _, ok := codexSetup.Managed["api_key"]; ok {
		t.Fatalf("Codex api_key was not removed: %#v", codexSetup.Managed)
	}
	if codexSetup.Managed["base_url"] != "https://codex.example" {
		t.Fatalf("Codex base_url = %q", codexSetup.Managed["base_url"])
	}

	acpConfig, _ := metadataRecord(metadata[MetadataKeyACP])
	agents, _ := metadataRecord(acpConfig["agents"])
	agentConfig, _ := metadataRecord(agents["custom-agent"])
	managed, _ := metadataRecord(agentConfig["managed"])
	if _, ok := managed["api_key"].(map[string]any); !ok {
		t.Fatalf("original metadata mutated: %#v", managed)
	}
}

func TestMergeSensitiveFieldsThreeState(t *testing.T) {
	existing := map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				"codex": map[string]any{
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
				"codex": map[string]any{
					"enabled": true,
					"managed": map[string]any{
						"base_url": "https://new.example",
					},
				},
			},
		},
	})
	if got := ParseAgentSetup(preserve, "codex").Managed["api_key"]; got != "sk-existing" {
		t.Fatalf("missing api_key update preserved %q, want existing secret", got)
	}

	cleared := MergeSensitiveFieldsForUpdate(existing, map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				"codex": map[string]any{
					"enabled": true,
					"managed": map[string]any{
						"api_key": nil,
					},
				},
			},
		},
	})
	if _, ok := ParseAgentSetup(cleared, "codex").Managed["api_key"]; ok {
		t.Fatalf("nil api_key update should clear existing secret")
	}

	overwritten := MergeSensitiveFieldsForUpdate(existing, map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				"codex": map[string]any{
					"enabled": true,
					"managed": map[string]any{
						"api_key": "sk-new",
					},
				},
			},
		},
	})
	if got := ParseAgentSetup(overwritten, "codex").Managed["api_key"]; got != "sk-new" {
		t.Fatalf("new api_key update = %q, want overwrite", got)
	}

	dottedSecret := MergeSensitiveFieldsForUpdate(existing, map[string]any{
		MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				"codex": map[string]any{
					"enabled": true,
					"managed": map[string]any{
						"api_key": "https://acme.example.com/v1/...",
					},
				},
			},
		},
	})
	if got := ParseAgentSetup(dottedSecret, "codex").Managed["api_key"]; got != "https://acme.example.com/v1/..." {
		t.Fatalf("dotted api_key update = %q, want literal value", got)
	}
}
