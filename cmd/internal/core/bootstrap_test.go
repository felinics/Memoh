package core

import (
	"testing"

	"github.com/felinics/memoh/internal/registry"
)

func TestRegistryProviderTemplatesKeepAllProviderFiles(t *testing.T) {
	defs := []registry.ProviderDefinition{
		{
			Name:       "DeepSeek",
			ClientType: "openai-completions",
		},
		{
			Name:       "OpenAI",
			ClientType: "openai-responses",
		},
		{
			Name:       "OpenAI Speech",
			ClientType: "openai-speech",
		},
		{
			Name:       "Google Transcription",
			ClientType: "google-transcription",
		},
	}

	got := registry.ProviderTemplateDefinitions(defs)
	if len(got) != len(defs) {
		t.Fatalf("definition count = %d, want %d", len(got), len(defs))
	}
	for i := range defs {
		if got[i].Name != defs[i].Name {
			t.Fatalf("definition %d = %#v, want %#v", i, got[i], defs[i])
		}
	}
}
