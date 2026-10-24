package registry

import (
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
)

func TestProviderTemplateDefinitionsKeepLastDuplicateModel(t *testing.T) {
	t.Parallel()

	definitions := ProviderTemplateDefinitions([]ProviderDefinition{{
		Name:       "OpenRouter",
		ClientType: "openai-completions",
		Source:     "openrouter.yaml",
		Models: []ModelDefinition{
			{ModelID: "openrouter/auto", Name: "Old Auto", Type: "chat"},
			{ModelID: "other/model", Name: "Other", Type: "chat"},
			{ModelID: "openrouter/auto", Name: "Auto Router", Type: "chat"},
		},
	}})

	if len(definitions) != 1 {
		t.Fatalf("template count = %d, want 1", len(definitions))
	}
	models := definitions[0].Models
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2: %#v", len(models), models)
	}
	if models[1].ModelID != "openrouter/auto" || models[1].Name != "Auto Router" || models[1].SortOrder != 2 {
		t.Fatalf("deduplicated model = %#v", models[1])
	}
}

func TestFishAudioTemplatesStayInTheirAudioDomains(t *testing.T) {
	t.Parallel()

	definitions, err := Load(slog.New(slog.DiscardHandler), filepath.Join("..", "..", "conf", "providers"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		key, domain, modelType string
		modelIDs               []string
	}{
		{"fishaudio-speech", "speech", "speech", []string{"s2.1-pro", "s2.1-pro-free", "drama-3"}},
		{"fishaudio-transcription", "transcription", "transcription", []string{"transcribe-1-pro", "transcribe-1"}},
	} {
		t.Run(tt.key, func(t *testing.T) {
			for _, template := range ProviderTemplateDefinitions(definitions) {
				if template.Key != tt.key {
					continue
				}
				if string(template.Domain) != tt.domain || template.Driver != tt.key {
					t.Fatalf("template classified as %s / %s", template.Domain, template.Driver)
				}
				if template.DefaultConfig["base_url"] != "https://api.fish.audio/v1" {
					t.Fatalf("unexpected default endpoint: %v", template.DefaultConfig)
				}
				var ids []string
				for _, model := range template.Models {
					if model.Type != tt.modelType {
						t.Fatalf("model %s has type %s", model.ModelID, model.Type)
					}
					ids = append(ids, model.ModelID)
				}
				if !slices.Equal(ids, tt.modelIDs) {
					t.Fatalf("template imports %v, want %v", ids, tt.modelIDs)
				}
				return
			}
			t.Fatal("Fish Audio template missing")
		})
	}
}

func TestMoonshotTemplateUsesExplicitKimiChatCompletionsCompat(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)
	definitions, err := Load(logger, filepath.Join("..", "..", "conf", "providers"))
	if err != nil {
		t.Fatalf("load provider templates: %v", err)
	}
	for _, definition := range definitions {
		if definition.Source != "moonshot.yaml" {
			continue
		}
		if got := definition.Config["chat_completions_compat"]; got != "kimi" {
			t.Fatalf("Moonshot chat_completions_compat = %#v, want kimi", got)
		}
		return
	}
	t.Fatal("Moonshot provider template not found")
}
