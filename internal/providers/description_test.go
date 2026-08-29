package providers

import (
	"testing"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/registry"
)

func TestRemoteModelsFromTemplateIncludesOptionalDescription(t *testing.T) {
	t.Parallel()

	models := remoteModelsFromTemplate(registry.ProviderDefinition{
		Models: []registry.ModelDefinition{{
			ModelID: "gpt-test",
			Name:    "GPT Test",
			Type:    "chat",
			Config: map[string]any{
				"description": "  Template description.  ",
			},
		}},
	})

	if len(models) != 1 {
		t.Fatalf("models count = %d, want 1", len(models))
	}
	if models[0].Description == nil || *models[0].Description != "Template description." {
		t.Fatalf("description = %v, want trimmed template description", models[0].Description)
	}
}

func TestRemoteModelsFromTemplateKeepsZeroBudgetAndDefaultOn(t *testing.T) {
	t.Parallel()

	models := remoteModelsFromTemplate(registry.ProviderDefinition{
		Models: []registry.ModelDefinition{{
			ModelID: "gemini-test",
			Name:    "Gemini Test",
			Type:    "chat",
			Config: map[string]any{
				"reasoning_default_on": false,
				"thinking_budget_min":  0,
				"thinking_budget_max":  24576,
			},
		}},
	})

	if len(models) != 1 {
		t.Fatalf("models count = %d, want 1", len(models))
	}
	if models[0].ReasoningDefaultOn == nil || *models[0].ReasoningDefaultOn {
		t.Fatalf("reasoning_default_on = %v, want false", models[0].ReasoningDefaultOn)
	}
	if models[0].ThinkingBudgetMin == nil || *models[0].ThinkingBudgetMin != 0 {
		t.Fatalf("thinking_budget_min = %v, want 0", models[0].ThinkingBudgetMin)
	}
	if models[0].ThinkingBudgetMax == nil || *models[0].ThinkingBudgetMax != 24576 {
		t.Fatalf("thinking_budget_max = %v, want 24576", models[0].ThinkingBudgetMax)
	}
}

func TestRemoteModelsFromCatalogKeepsZeroBudgetAndDefaultOn(t *testing.T) {
	t.Parallel()

	models := remoteModelsFromCatalog([]sqlc.TemplateProviderTemplateModel{{
		ModelID: "gemini-test",
		Name:    "Gemini Test",
		Type:    "chat",
		Config:  []byte(`{"reasoning_default_on":false,"thinking_budget_min":0,"thinking_budget_max":24576}`),
	}})

	if len(models) != 1 {
		t.Fatalf("models count = %d, want 1", len(models))
	}
	if models[0].ReasoningDefaultOn == nil || *models[0].ReasoningDefaultOn {
		t.Fatalf("reasoning_default_on = %v, want false", models[0].ReasoningDefaultOn)
	}
	if models[0].ThinkingBudgetMin == nil || *models[0].ThinkingBudgetMin != 0 {
		t.Fatalf("thinking_budget_min = %v, want 0", models[0].ThinkingBudgetMin)
	}
	if models[0].ThinkingBudgetMax == nil || *models[0].ThinkingBudgetMax != 24576 {
		t.Fatalf("thinking_budget_max = %v, want 24576", models[0].ThinkingBudgetMax)
	}
}
