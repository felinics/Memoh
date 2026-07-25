package provider

import (
	"testing"

	"github.com/memohai/memoh/domains/model/template"
)

func TestRemoteModelsFromTemplateIncludesOptionalDescription(t *testing.T) {
	t.Parallel()

	models := remoteModelsFromTemplate(template.Definition{
		Models: []template.ModelDefinition{{
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
