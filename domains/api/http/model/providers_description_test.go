package model

import (
	"testing"

	modeldomain "github.com/memohai/memoh/domains/model"
)

func descriptionPointer(value string) *string { return &value }

func TestMergeDiscoveredConfigFillsOnlyMissingDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		existing   *string
		discovered *string
		want       *string
		changed    bool
	}{
		{name: "fills missing", discovered: descriptionPointer("Template"), want: descriptionPointer("Template"), changed: true},
		{name: "preserves user value", existing: descriptionPointer("Custom"), discovered: descriptionPointer("Template"), want: descriptionPointer("Custom")},
		{name: "preserves explicit clear", existing: descriptionPointer(""), discovered: descriptionPointer("Template"), want: descriptionPointer("")},
		{name: "ignores missing discovery"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := mergeDiscoveredConfig(
				modeldomain.ModelConfig{Description: tt.existing},
				modeldomain.ModelConfig{Description: tt.discovered},
			)
			if changed != tt.changed {
				t.Fatalf("changed = %v, want %v", changed, tt.changed)
			}
			if tt.want == nil {
				if got.Description != nil {
					t.Fatalf("description = %q, want nil", *got.Description)
				}
				return
			}
			if got.Description == nil || *got.Description != *tt.want {
				t.Fatalf("description = %v, want %q", got.Description, *tt.want)
			}
		})
	}
}

func TestMergeManagedDiscoveredConfigReplacesCapabilities(t *testing.T) {
	t.Parallel()

	existing := modeldomain.ModelConfig{
		Compatibilities:  []string{modeldomain.CompatVision, modeldomain.CompatToolCall, modeldomain.CompatReasoning},
		ReasoningEfforts: []string{modeldomain.ReasoningEffortHigh},
		ThinkingMode:     modeldomain.ThinkingModeToggle,
	}
	discovered := modeldomain.ModelConfig{
		Compatibilities: []string{modeldomain.CompatToolCall},
		ThinkingMode:    modeldomain.ThinkingModeNone,
	}

	got, changed := mergeManagedDiscoveredConfig(existing, discovered)
	if !changed {
		t.Fatal("expected managed capability refresh to report a change")
	}
	if len(got.Compatibilities) != 1 || got.Compatibilities[0] != modeldomain.CompatToolCall {
		t.Fatalf("compatibilities = %#v", got.Compatibilities)
	}
	if len(got.ReasoningEfforts) != 0 {
		t.Fatalf("reasoning efforts = %#v, want empty", got.ReasoningEfforts)
	}
	if got.ThinkingMode != modeldomain.ThinkingModeNone {
		t.Fatalf("thinking mode = %q, want none", got.ThinkingMode)
	}
}
