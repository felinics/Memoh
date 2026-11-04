package profile

import (
	"slices"
	"strings"
	"testing"
)

func TestResolveGenericACPLaunch(t *testing.T) {
	command, arguments, err := ResolveLaunch(AgentSetup{Managed: map[string]string{
		"command":   "  uvx  ",
		"arguments": "agent-package\r\n--mode\nvalue with spaces\n\n",
	}})
	if err != nil || command != "uvx" || !slices.Equal(arguments, []string{"agent-package", "--mode", "value with spaces"}) {
		t.Fatalf("ResolveLaunch() = %q, %#v, %v", command, arguments, err)
	}
	if _, _, err := ResolveLaunch(AgentSetup{Managed: map[string]string{"command": "uvx\nagent"}}); err == nil {
		t.Fatal("a command spanning lines was accepted")
	}
}

func TestParseAgentSetupNormalizesLegacyManagedMode(t *testing.T) {
	legacy := func(agentID string, managed map[string]any) map[string]any {
		return map[string]any{MetadataKeyACP: map[string]any{"agents": map[string]any{
			agentID: map[string]any{"enabled": true, "setup_mode": "managed", "managed": managed},
		}}}
	}
	if got := ParseAgentSetup(legacy("custom-agent", map[string]any{"api_key": "AIza-test"}), "custom-agent").Mode; got != setupModeAPIKey {
		t.Fatalf("legacy managed mode = %q, want api_key", got)
	}
	if got := ParseAgentSetup(legacy("codex", map[string]any{"auth_type": "provider_oauth"}), "codex").Mode; got != setupModeOAuth {
		t.Fatalf("legacy provider_oauth mode = %q, want oauth", got)
	}
}

func TestCheckSetupRequiresTheCommandAndASupportedMode(t *testing.T) {
	setup := func(mode string, managed map[string]string) AgentSetup {
		return AgentSetup{AgentID: AgentACPID, Enabled: true, Mode: mode, Managed: managed}
	}
	if err := CheckSetup(setup(setupModeAPIKey, map[string]string{})); err == nil || !strings.Contains(err.Error(), "command required") {
		t.Fatalf("missing command: error = %v", err)
	}
	if err := CheckSetup(setup(setupModeSelf, map[string]string{"command": "my-agent-acp"})); err == nil || !strings.Contains(err.Error(), "setup mode") {
		t.Fatalf("legacy self mode: error = %v, want unsupported mode", err)
	}
	unset := map[string]any{MetadataKeyACP: map[string]any{"agents": map[string]any{
		AgentACPID: map[string]any{"enabled": true, "managed": map[string]any{"command": "my-agent-acp"}},
	}}}
	if err := CheckSetup(ParseAgentSetup(unset, AgentACPID)); err != nil {
		t.Fatalf("setup without a setup_mode: error = %v", err)
	}
}

func managedUpdate(managed map[string]any) map[string]any {
	return map[string]any{MetadataKeyACP: map[string]any{"agents": map[string]any{
		"codex": map[string]any{"enabled": true, "managed": managed},
	}}}
}

// A secret omitted or sent back masked keeps the stored one; null clears it;
// any other value, dotted or not, replaces it. Responses carry it masked.
func TestMergeSensitiveFields(t *testing.T) {
	existing := managedUpdate(map[string]any{"api_key": "sk-existing", "base_url": "https://old.example"})
	for _, tt := range []struct {
		name    string
		managed map[string]any
		want    string
		cleared bool
	}{
		{name: "omitted", managed: map[string]any{"base_url": "https://new.example"}, want: "sk-existing"},
		{name: "masked", managed: map[string]any{"api_key": "sk-...ting"}, want: "sk-existing"},
		{name: "null", managed: map[string]any{"api_key": nil}, cleared: true},
		{name: "new", managed: map[string]any{"api_key": "sk-new"}, want: "sk-new"},
		{name: "dotted", managed: map[string]any{"api_key": "https://acme.example.com/v1/..."}, want: "https://acme.example.com/v1/..."},
	} {
		merged := MergeSensitiveFieldsForUpdate(existing, managedUpdate(tt.managed))
		got, ok := ParseAgentSetup(merged, "codex").Managed["api_key"]
		if tt.cleared == ok || (!tt.cleared && got != tt.want) {
			t.Fatalf("%s: api_key = %q (present %v)", tt.name, got, ok)
		}
	}
	scrubbed := ParseAgentSetup(ScrubMetadataForResponse(existing), "codex").Managed["api_key"]
	if scrubbed == "sk-existing" || scrubbed == "" {
		t.Fatalf("response api_key = %q, want masked", scrubbed)
	}
}

func TestScrubMetadataForExportDropsManagedSecrets(t *testing.T) {
	metadata := map[string]any{MetadataKeyACP: map[string]any{"agents": map[string]any{
		"custom-agent": map[string]any{"enabled": true, "setup_mode": "api_key", "managed": map[string]any{
			"model": "anthropic/claude-sonnet-4", "api_key": map[string]any{"value": "custom-secret"},
		}},
		"codex": map[string]any{"enabled": true, "managed": map[string]any{"api_key": "sk-codex", "base_url": "https://codex.example"}},
	}}}

	scrubbed, changed := ScrubMetadataForExport(metadata)
	custom, codex := ParseAgentSetup(scrubbed, "custom-agent").Managed, ParseAgentSetup(scrubbed, "codex").Managed
	if _, ok := custom["api_key"]; !changed || ok || custom["model"] != "anthropic/claude-sonnet-4" {
		t.Fatalf("custom agent managed = %#v (changed %v)", custom, changed)
	}
	if _, ok := codex["api_key"]; ok || codex["base_url"] != "https://codex.example" {
		t.Fatalf("codex managed = %#v", codex)
	}
	acpConfig, _ := metadataRecord(metadata[MetadataKeyACP])
	agents, _ := metadataRecord(acpConfig["agents"])
	agentConfig, _ := metadataRecord(agents["custom-agent"])
	managed, _ := metadataRecord(agentConfig["managed"])
	if _, ok := managed["api_key"].(map[string]any); !ok {
		t.Fatalf("original metadata mutated: %#v", managed)
	}
}
