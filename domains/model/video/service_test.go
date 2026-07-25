package video

import "testing"

func TestMaskProviderConfigMasksSchemaSecrets(t *testing.T) {
	cfg := map[string]any{
		"api_key":  "sk-secret-123456",
		"base_url": "https://example.test",
	}
	masked := maskProviderConfig(cfg, ConfigSchema{Fields: []FieldSchema{
		{Key: "api_key", Type: "secret"},
		{Key: "base_url", Type: "string"},
	}})

	if got, _ := masked["api_key"].(string); got == "" || got == cfg["api_key"] {
		t.Fatalf("api_key was not masked: %q", got)
	}
	if masked["base_url"] != cfg["base_url"] {
		t.Fatalf("base_url = %v, want %v", masked["base_url"], cfg["base_url"])
	}
	if cfg["api_key"] != "sk-secret-123456" {
		t.Fatalf("source config was mutated: %v", cfg["api_key"])
	}
}
