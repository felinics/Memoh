package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	acpclient "github.com/memohai/memoh/domains/agent/acp/client"
	acpprofile "github.com/memohai/memoh/domains/agent/acp/profile"
	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/api/http/httpfixture"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
)

func TestPrepareACPWorkspaceConfigWritesCodexAPIKeyConfig(t *testing.T) {
	client, recorder := httpfixture.NewACPConfigBridgeClient(t)
	handler := &UsersHandler{
		acpWorkspace: &httpfixture.ACPConfigWorkspace{
			Backend: bridge.WorkspaceBackendContainer,
			Client:  client,
		},
	}

	err := handler.prepareACPWorkspaceConfig(context.Background(), bot.Bot{
		ID: "bot-1",
		Metadata: map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{
						"enabled":    true,
						"setup_mode": "api_key",
						"managed": map[string]any{
							"api_key":  "sk-secret",
							"base_url": "https://proxy.example.com/v1",
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("prepareACPWorkspaceConfig() error = %v", err)
	}

	writes := recorder.Writes()
	if len(writes) != 2 {
		t.Fatalf("writes len = %d, want config.toml + auth.json: %#v", len(writes), writes)
	}
	configWrite, ok := httpfixture.FindACPConfigWrite(writes, acpclient.CodexManagedConfigDir+"/config.toml")
	if !ok {
		t.Fatalf("missing Codex config.toml write: %#v", writes)
	}
	content := string(configWrite.Content)
	for _, want := range []string{
		`model_provider = "OpenAI"`,
		`[model_providers.OpenAI]`,
		`base_url = "https://proxy.example.com/v1"`,
		`wire_api = "responses"`,
		`requires_openai_auth = false`,
		`supports_websockets = false`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("config missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "sk-secret") || strings.Contains(content, "api_key") {
		t.Fatalf("config leaked API key:\n%s", content)
	}
	authWrite, ok := httpfixture.FindACPConfigWrite(writes, acpclient.CodexManagedConfigDir+"/auth.json")
	if !ok {
		t.Fatalf("missing Codex auth.json write: %#v", writes)
	}
	auth := string(authWrite.Content)
	if !strings.Contains(auth, `"OPENAI_API_KEY": "sk-secret"`) {
		t.Fatalf("auth missing API key:\n%s", auth)
	}
}

func TestPrepareACPWorkspaceConfigSkipsCodexOAuthConfig(t *testing.T) {
	client, recorder := httpfixture.NewACPConfigBridgeClient(t)
	handler := &UsersHandler{
		acpWorkspace: &httpfixture.ACPConfigWorkspace{
			Backend: bridge.WorkspaceBackendContainer,
			Client:  client,
		},
	}

	err := handler.prepareACPWorkspaceConfig(context.Background(), bot.Bot{
		ID: "bot-1",
		Metadata: map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{
						"enabled":    true,
						"setup_mode": "oauth",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("prepareACPWorkspaceConfig() error = %v", err)
	}
	if writes := recorder.Writes(); len(writes) != 0 {
		t.Fatalf("OAuth setup should be written only by ACP Codex OAuth callback, got writes: %#v", writes)
	}
}

func TestPrepareACPWorkspaceConfigSkipsSelf(t *testing.T) {
	handler := &UsersHandler{acpWorkspace: &httpfixture.ACPConfigWorkspace{Backend: bridge.WorkspaceBackendContainer}}
	selfBot := bot.Bot{
		ID: "bot-1",
		Metadata: map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{
						"enabled":    true,
						"setup_mode": "self",
					},
				},
			},
		},
	}
	if err := handler.prepareACPWorkspaceConfig(context.Background(), selfBot); err != nil {
		t.Fatalf("self setup should be skipped: %v", err)
	}
}

func TestPrepareACPWorkspaceConfigSurfacesWriteErrors(t *testing.T) {
	handler := &UsersHandler{
		acpWorkspace: &httpfixture.ACPConfigWorkspace{
			Backend: bridge.WorkspaceBackendContainer,
			MCPErr:  errors.New("bridge unavailable"),
		},
	}

	err := handler.prepareACPWorkspaceConfig(context.Background(), bot.Bot{
		ID: "bot-1",
		Metadata: map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentHermesID: map[string]any{
						"enabled":    true,
						"setup_mode": "api_key",
						"managed": map[string]any{
							"provider": "openrouter",
							"model":    "anthropic/claude-sonnet-4",
							"api_key":  "sk-hermes",
						},
					},
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "bridge unavailable") {
		t.Fatalf("prepareACPWorkspaceConfig() error = %v, want bridge unavailable", err)
	}
}

func TestValidateACPManagedConfigRejectsInvalidHermesCustom(t *testing.T) {
	err := validateACPManagedConfig(map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentHermesID: map[string]any{
					"enabled":    true,
					"setup_mode": "api_key",
					"managed": map[string]any{
						"provider": "custom",
						"model":    "my-model",
						"api_key":  "sk-hermes",
					},
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "base_url required") {
		t.Fatalf("validateACPManagedConfig() error = %v, want base_url required", err)
	}
}

func TestValidateACPManagedConfigRejectsUnsupportedHermesSetupMode(t *testing.T) {
	err := validateACPManagedConfig(map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentHermesID: map[string]any{
					"enabled":    true,
					"setup_mode": "oauth",
					"managed": map[string]any{
						"provider": "gemini",
						"model":    "gemini-3.5-flash",
						"api_key":  "AIza-test",
					},
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), `Hermes does not support setup mode "oauth"`) {
		t.Fatalf("validateACPManagedConfig() error = %v, want unsupported setup mode", err)
	}
}

func TestValidateACPManagedConfigAcceptsLegacyHermesManagedMode(t *testing.T) {
	err := validateACPManagedConfig(map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentHermesID: map[string]any{
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
	})
	if err != nil {
		t.Fatalf("validateACPManagedConfig() error = %v, want legacy managed accepted as api_key", err)
	}
}

func TestValidateACPManagedConfigAcceptsLegacyCodexManagedOAuthMode(t *testing.T) {
	err := validateACPManagedConfig(map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentCodexID: map[string]any{
					"enabled":    true,
					"setup_mode": "managed",
					"managed": map[string]any{
						"auth_type": "provider_oauth",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("validateACPManagedConfig() error = %v, want legacy managed provider_oauth accepted as oauth", err)
	}
}

func TestACPManagedConfigNeedsWriteOnlyWhenManagedTargetChanges(t *testing.T) {
	existing := map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentHermesID: map[string]any{
					"enabled":    true,
					"setup_mode": "api_key",
					"managed": map[string]any{
						"provider": "openrouter",
						"model":    "anthropic/claude-sonnet-4",
						"api_key":  "sk-existing",
					},
				},
			},
		},
		"unrelated": "old",
	}
	unchanged := acpprofile.MergeSensitiveFieldsForUpdate(existing, map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentHermesID: map[string]any{
					"enabled":    true,
					"setup_mode": "api_key",
					"managed": map[string]any{
						"provider": "openrouter",
						"model":    "anthropic/claude-sonnet-4",
						"api_key":  "sk-...ting",
					},
				},
			},
		},
		"unrelated": "new",
	})
	if acpManagedConfigNeedsWrite(existing, unchanged) {
		t.Fatal("unchanged managed config should not require workspace write")
	}

	changed := acpprofile.MergeSensitiveFieldsForUpdate(existing, map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentHermesID: map[string]any{
					"enabled":    true,
					"setup_mode": "api_key",
					"managed": map[string]any{
						"provider": "openrouter",
						"model":    "openrouter/auto",
						"api_key":  "sk-...ting",
					},
				},
			},
		},
	})
	if !acpManagedConfigNeedsWrite(existing, changed) {
		t.Fatal("changed managed model should require workspace write")
	}
}

func TestACPRuntimeMetadataChangedIgnoresUnrelatedMetadata(t *testing.T) {
	existing := map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentHermesID: map[string]any{
					"enabled":    true,
					"setup_mode": "api_key",
					"managed": map[string]any{
						"provider": "openrouter",
						"model":    "anthropic/claude-sonnet-4",
					},
				},
			},
		},
		"unrelated": "old",
	}
	unchanged := map[string]any{
		acpprofile.MetadataKeyACP: existing[acpprofile.MetadataKeyACP],
		"unrelated":               "new",
	}
	if acpRuntimeMetadataChanged(existing, unchanged) {
		t.Fatal("unrelated metadata change should not close ACP runtimes")
	}
	changed := map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentHermesID: map[string]any{
					"enabled":    false,
					"setup_mode": "api_key",
					"managed": map[string]any{
						"provider": "openrouter",
						"model":    "anthropic/claude-sonnet-4",
					},
				},
			},
		},
	}
	if !acpRuntimeMetadataChanged(existing, changed) {
		t.Fatal("ACP metadata change should close ACP runtimes")
	}
}

func TestPrepareACPWorkspaceConfigWritesHermesManagedConfig(t *testing.T) {
	client, recorder := httpfixture.NewACPConfigBridgeClient(t)
	handler := &UsersHandler{
		acpWorkspace: &httpfixture.ACPConfigWorkspace{
			Backend: bridge.WorkspaceBackendContainer,
			Client:  client,
		},
	}

	err := handler.prepareACPWorkspaceConfig(context.Background(), bot.Bot{
		ID: "bot-1",
		Metadata: map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentHermesID: map[string]any{
						"enabled":    true,
						"setup_mode": "api_key",
						"managed": map[string]any{
							"provider": "openrouter",
							"model":    "anthropic/claude-sonnet-4",
							"api_key":  "sk-hermes",
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("prepareACPWorkspaceConfig() error = %v", err)
	}

	writes := recorder.Writes()
	configWrite, ok := httpfixture.FindACPConfigWrite(writes, acpclient.HermesContainerHome+"/config.yaml")
	if !ok {
		t.Fatalf("missing Hermes config.yaml write: %#v", writes)
	}
	if !strings.Contains(string(configWrite.Content), `provider: "openrouter"`) ||
		!strings.Contains(string(configWrite.Content), `default: "anthropic/claude-sonnet-4"`) {
		t.Fatalf("Hermes config content =\n%s", string(configWrite.Content))
	}
	envWrite, ok := httpfixture.FindACPConfigWrite(writes, acpclient.HermesContainerHome+"/.env")
	if !ok {
		t.Fatalf("missing Hermes .env write: %#v", writes)
	}
	if !strings.Contains(string(envWrite.Content), `OPENROUTER_API_KEY='sk-hermes'`) {
		t.Fatalf("Hermes env content =\n%s", string(envWrite.Content))
	}
}
