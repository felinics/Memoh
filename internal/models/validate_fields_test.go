package models

import (
	"errors"
	"testing"
)

func TestModelValidateNamesTheOffendingField(t *testing.T) {
	t.Parallel()
	const provider = "11111111-1111-1111-1111-111111111111"
	zero := 0
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
		{"compatibilities", Model{ModelID: "m", ProviderID: provider, Type: ModelTypeChat, Config: ModelConfig{Compatibilities: []string{"bogus"}}}, false, "config.compatibilities"},
		{"reasoning efforts", Model{ModelID: "m", ProviderID: provider, Type: ModelTypeChat, Config: ModelConfig{ReasoningEfforts: []string{"bogus"}}}, false, "config.reasoning_efforts"},
		{"thinking mode", Model{ModelID: "m", ProviderID: provider, Type: ModelTypeChat, Config: ModelConfig{ThinkingMode: "bogus"}}, false, "config.thinking_mode"},
		{"reasoning dialect", Model{ModelID: "m", ProviderID: provider, Type: ModelTypeChat, Config: ModelConfig{ReasoningDialect: "bogus"}}, false, "config.reasoning_dialect"},
		{"reasoning off support", Model{ModelID: "m", ProviderID: provider, Type: ModelTypeChat, Config: ModelConfig{ReasoningOffSupport: "bogus"}}, false, "config.reasoning_off_support"},
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
