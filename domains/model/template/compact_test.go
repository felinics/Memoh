package template

import "testing"

func TestCompactModelsKeepLastDuplicateModel(t *testing.T) {
	t.Parallel()

	definitions := compactModels([]Definition{{
		Name:   "OpenRouter",
		Driver: "openai-completions",
		Source: "openrouter.yaml",
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
