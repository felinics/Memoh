package profile

import (
	"errors"
	"testing"
)

func TestAPIKeyProbeTargetCodexDefaults(t *testing.T) {
	target, err := APIKeyProbeTarget(AgentSetup{
		AgentID: AgentCodexID,
		Mode:    setupModeAPIKey,
		Managed: map[string]string{"api_key": "sk-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if target.ClientType != "openai-responses" {
		t.Fatalf("client type = %q", target.ClientType)
	}
	if target.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("base url = %q", target.BaseURL)
	}
	if target.APIKey != "sk-test" {
		t.Fatalf("api key = %q", target.APIKey)
	}
}

func TestAPIKeyProbeTargetCodexCustomBaseURL(t *testing.T) {
	target, err := APIKeyProbeTarget(AgentSetup{
		AgentID: AgentCodexID,
		Mode:    setupModeAPIKey,
		Managed: map[string]string{
			"api_key":  " sk-test ",
			"base_url": " https://proxy.example.com/v1/ ",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if target.BaseURL != "https://proxy.example.com/v1" {
		t.Fatalf("base url = %q", target.BaseURL)
	}
	if target.APIKey != "sk-test" {
		t.Fatalf("api key = %q", target.APIKey)
	}
}

func TestAPIKeyProbeTargetClaudeCodeDefaults(t *testing.T) {
	target, err := APIKeyProbeTarget(AgentSetup{
		AgentID: AgentClaudeCodeID,
		Mode:    setupModeAPIKey,
		Managed: map[string]string{"api_key": "sk-ant-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if target.ClientType != "anthropic-messages" {
		t.Fatalf("client type = %q", target.ClientType)
	}
	if target.BaseURL != "https://api.anthropic.com/v1" {
		t.Fatalf("base url = %q", target.BaseURL)
	}
	if target.APIKey != "sk-ant-test" {
		t.Fatalf("api key = %q", target.APIKey)
	}
}

func TestAPIKeyProbeTargetClaudeCodeAppendsV1ToCustomBaseURL(t *testing.T) {
	target, err := APIKeyProbeTarget(AgentSetup{
		AgentID: AgentClaudeCodeID,
		Mode:    setupModeAPIKey,
		Managed: map[string]string{
			"api_key":  "sk-ant-test",
			"base_url": "https://cpa.example.com/",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if target.BaseURL != "https://cpa.example.com/v1" {
		t.Fatalf("base url = %q", target.BaseURL)
	}
}

func TestAPIKeyProbeTargetRejectsNonAPIKeyModes(t *testing.T) {
	for _, mode := range []string{setupModeOAuth, setupModeSelf} {
		_, err := APIKeyProbeTarget(AgentSetup{
			AgentID: AgentClaudeCodeID,
			Mode:    mode,
			Managed: map[string]string{"api_key": "sk-ant-test"},
		})
		if !errors.Is(err, ErrCredentialProbeNotAPIKeyMode) {
			t.Fatalf("mode %s error = %v", mode, err)
		}
	}
}

func TestAPIKeyProbeTargetRequiresAPIKey(t *testing.T) {
	_, err := APIKeyProbeTarget(AgentSetup{
		AgentID: AgentCodexID,
		Mode:    setupModeAPIKey,
		Managed: map[string]string{"api_key": "  "},
	})
	if !errors.Is(err, ErrCredentialProbeAPIKeyMissing) {
		t.Fatalf("error = %v", err)
	}
}

func TestAPIKeyProbeTargetUnsupportedAgents(t *testing.T) {
	for _, agentID := range []string{AgentHermesID, "unknown-agent"} {
		_, err := APIKeyProbeTarget(AgentSetup{
			AgentID: agentID,
			Mode:    setupModeAPIKey,
			Managed: map[string]string{"api_key": "sk-test"},
		})
		if !errors.Is(err, ErrCredentialProbeUnsupported) {
			t.Fatalf("agent %s error = %v", agentID, err)
		}
	}
}

func TestAPIKeyProbeTargetFromParsedMetadata(t *testing.T) {
	metadata := map[string]any{
		"acp": map[string]any{
			"agents": map[string]any{
				"claude-code": map[string]any{
					"enabled":    true,
					"setup_mode": "api_key",
					"managed": map[string]any{
						"api_key":  "sk-ant-test",
						"base_url": "https://gateway.example.com",
					},
				},
			},
		},
	}
	target, err := APIKeyProbeTarget(ParseAgentSetup(metadata, AgentClaudeCodeID))
	if err != nil {
		t.Fatal(err)
	}
	if target.ClientType != "anthropic-messages" || target.BaseURL != "https://gateway.example.com/v1" || target.APIKey != "sk-ant-test" {
		t.Fatalf("target = %#v", target)
	}
}
