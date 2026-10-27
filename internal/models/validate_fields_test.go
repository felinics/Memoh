package models

import (
	"errors"
	"testing"
)

func TestModelValidateNamesTheOffendingField(t *testing.T) {
	t.Parallel()
	const provider = "11111111-1111-1111-1111-111111111111"
	zero, one, ten := 0, 1, 10
	cases := []struct {
		name     string
		model    Model
		required bool
		field    string
	}{
		{"model id", Model{ProviderID: provider, Type: ModelTypeChat}, true, "model_id"},
		{"provider id", Model{ModelID: "m", Type: ModelTypeChat}, true, "provider_id"},
		{"provider uuid", Model{ModelID: "m", ProviderID: "x", Type: ModelTypeChat}, false, "provider_id"},
		{"type", Model{ModelID: "m", ProviderID: provider, Type: "bogus"}, false, "type"},
		{"dimensions", Model{ModelID: "m", ProviderID: provider, Type: ModelTypeEmbedding, Config: ModelConfig{Dimensions: &zero}}, true, "config.dimensions"},
		{"modalities", Model{ModelID: "m", ProviderID: provider, Type: ModelTypeChat, Config: ModelConfig{Modalities: &ModelModalities{Input: []Modality{"bogus"}}}}, false, "config.modalities"},
		{"reasoning efforts", Model{ModelID: "m", ProviderID: provider, Type: ModelTypeChat, Config: ModelConfig{Reasoning: &ModelReasoning{Efforts: []string{"bogus"}}}}, false, "config.reasoning.efforts"},
		{"disable is not an effort", Model{ModelID: "m", ProviderID: provider, Type: ModelTypeChat, Config: ModelConfig{Reasoning: &ModelReasoning{Efforts: []string{ReasoningEffortDisable}}}}, false, "config.reasoning.efforts"},
		{"reasoning dialect", Model{ModelID: "m", ProviderID: provider, Type: ModelTypeChat, Config: ModelConfig{Reasoning: &ModelReasoning{Dialect: "bogus"}}}, false, "config.reasoning.dialect"},
		{"reasoning off support", Model{ModelID: "m", ProviderID: provider, Type: ModelTypeChat, Config: ModelConfig{Reasoning: &ModelReasoning{Off: "bogus"}}}, false, "config.reasoning.off"},
		{"reasoning budget", Model{ModelID: "m", ProviderID: provider, Type: ModelTypeChat, Config: ModelConfig{Reasoning: &ModelReasoning{Budget: &ReasoningBudget{Min: &ten, Max: &one}}}}, false, "config.reasoning.budget"},
		{"reasoning default effort", Model{ModelID: "m", ProviderID: provider, Type: ModelTypeChat, Config: ModelConfig{Reasoning: &ModelReasoning{Default: &ReasoningDefault{Effort: "bogus"}}}}, false, "config.reasoning.default.effort"},
		{"reasoning replay field", Model{ModelID: "m", ProviderID: provider, Type: ModelTypeChat, Config: ModelConfig{Reasoning: &ModelReasoning{Replay: &ReasoningReplay{Field: "bogus"}}}}, false, "config.reasoning.replay.field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var fe *FieldError
			if err := tc.model.Validate(); !errors.As(err, &fe) {
				t.Fatalf("Validate() = %v, want *FieldError", err)
			}
			if fe.Field != tc.field || fe.Required != tc.required {
				t.Fatalf("got field %q required=%v, want %q required=%v", fe.Field, fe.Required, tc.field, tc.required)
			}
		})
	}
}
