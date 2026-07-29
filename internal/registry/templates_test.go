package registry

import (
	"log/slog"
	"path/filepath"
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
