package providers

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/openaicatalog"
)

func TestOpenAICatalogPreservesAccountCapabilities(t *testing.T) {
	var catalog []openaicatalog.Model
	err := json.Unmarshal([]byte(`[
		{"slug":"z","display_name":"Z model","visibility":"list","context_window":272000,"max_context_window":872000,"input_modalities":["text","image"],"supported_reasoning_levels":[{"effort":"low"},{"effort":"max"},{"effort":"ultra"}]},
		{"slug":"hidden","visibility":"hide"},
		{"slug":"a","visibility":"list","max_context_window":128000}
	]`), &catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, clientType := range []models.ClientType{models.ClientTypeOpenAIChatGPT, models.ClientTypeOpenAICodex} {
		t.Run(string(clientType), func(t *testing.T) {
			got := remoteModelsFromOpenAI(catalog, clientType)
			if len(got) != 2 || got[0].ID != "z" || got[1].ID != "a" {
				t.Fatalf("visible catalog order = %+v", got)
			}
			if got[0].Name != "Z model" || got[1].Name != "a" || got[0].OwnedBy != string(clientType) {
				t.Fatalf("catalog identity = %+v", got)
			}
			if *got[0].ContextWindow != 272000 || *got[1].ContextWindow != 128000 {
				t.Fatalf("context windows = %+v", got)
			}
			if !slices.Equal(got[0].Compatibilities, []string{models.CompatToolCall, models.CompatVision, models.CompatReasoning}) ||
				!slices.Equal(got[0].ReasoningEfforts, []string{"low", "max"}) || got[0].ThinkingMode != models.ThinkingModeToggle {
				t.Fatalf("capabilities = %+v", got[0])
			}
			for i, model := range got {
				if clientType == models.ClientTypeOpenAIChatGPT {
					if model.CatalogOrder == nil || *model.CatalogOrder != i {
						t.Fatalf("model %s lost catalog position", model.ID)
					}
				} else if model.CatalogOrder != nil {
					t.Fatal("Codex ordering policy changed")
				}
			}
		})
	}
}
