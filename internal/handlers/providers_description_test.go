package handlers

import (
	"slices"
	"testing"

	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/reasoning"
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
				models.ModelConfig{Description: tt.existing},
				models.ModelConfig{Description: tt.discovered},
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

func TestImportedCompatibilitiesRequireExplicitDefaultsForUnknownModels(t *testing.T) {
	t.Parallel()

	unknown := providers.RemoteModel{ID: "custom-chat"}
	if got := importedCompatibilities(unknown, models.ModelTypeChat, nil, true); len(got) != 0 {
		t.Fatalf("unknown capabilities = %#v, want none", got)
	}
	if got := importedCompatibilities(unknown, models.ModelTypeChat, []string{models.CompatToolCall}, true); len(got) != 1 || got[0] != models.CompatToolCall {
		t.Fatalf("explicit defaults = %#v", got)
	}

	trusted := providers.RemoteModel{
		ID: "template-chat", CapabilitiesKnown: true, Compatibilities: []string{models.CompatToolCall},
	}
	if got := importedCompatibilities(trusted, models.ModelTypeChat, []string{models.CompatVision}, false); len(got) != 1 || got[0] != models.CompatToolCall {
		t.Fatalf("trusted capabilities = %#v, want template values", got)
	}
	if got := importedCompatibilities(providers.RemoteModel{ID: "template-without-capabilities"}, models.ModelTypeChat, []string{models.CompatVision}, false); len(got) != 0 {
		t.Fatalf("template without capabilities = %#v, want none", got)
	}
}

func TestMergeManagedDiscoveredConfigReplacesCapabilities(t *testing.T) {
	t.Parallel()

	existing := models.ModelConfig{
		Compatibilities:     []string{models.CompatVision, models.CompatToolCall, models.CompatReasoning},
		ReasoningEfforts:    []string{models.ReasoningEffortDisable, models.ReasoningEffortHigh},
		ThinkingMode:        models.ThinkingModeToggle,
		ReasoningDialect:    reasoning.DialectTier,
		ReasoningOffSupport: reasoning.OffSupportAccepted,
		ReasoningDefaultOn:  boolPointer(true),
		ThinkingBudgetMin:   intPointer(128),
		ThinkingBudgetMax:   intPointer(32768),
	}
	discovered := models.ModelConfig{
		Compatibilities: []string{models.CompatToolCall},
		ThinkingMode:    models.ThinkingModeNone,
	}

	got, changed := mergeManagedDiscoveredConfig(existing, discovered)
	if !changed {
		t.Fatal("expected managed capability refresh to report a change")
	}
	if len(got.Compatibilities) != 1 || got.Compatibilities[0] != models.CompatToolCall {
		t.Fatalf("compatibilities = %#v", got.Compatibilities)
	}
	if len(got.ReasoningEfforts) != 0 {
		t.Fatalf("reasoning efforts = %#v, want empty", got.ReasoningEfforts)
	}
	if got.ThinkingMode != models.ThinkingModeNone {
		t.Fatalf("thinking mode = %q, want none", got.ThinkingMode)
	}
	if got.ReasoningDialect != "" || got.ReasoningOffSupport != "" || got.ReasoningDefaultOn != nil ||
		got.ThinkingBudgetMin != nil || got.ThinkingBudgetMax != nil {
		t.Fatalf("managed refresh kept revoked wire metadata: %+v", got)
	}
}

func TestMergeDiscoveredConfigRefreshesTrustedReasoningMetadata(t *testing.T) {
	t.Parallel()

	existing := models.ModelConfig{
		Compatibilities:  []string{models.CompatReasoning},
		ReasoningEfforts: []string{models.ReasoningEffortDisable, models.ReasoningEffortLow},
	}
	discovered := models.ModelConfig{
		ReasoningEfforts:    []string{models.ReasoningEffortLow, models.ReasoningEffortHigh},
		ThinkingMode:        models.ThinkingModeToggle,
		ReasoningDialect:    reasoning.DialectBudget,
		ReasoningOffSupport: reasoning.OffSupportRejected,
		ReasoningDefaultOn:  boolPointer(false),
		ThinkingBudgetMin:   intPointer(0),
		ThinkingBudgetMax:   intPointer(24576),
	}

	got, changed := mergeDiscoveredConfig(existing, discovered)
	if !changed {
		t.Fatal("expected trusted metadata refresh to report a change")
	}
	// Generic discovery cannot report out-of-band off support, so the template's
	// disable declaration survives while the active tiers refresh.
	if !slices.Equal(got.ReasoningEfforts, []string{
		models.ReasoningEffortDisable,
		models.ReasoningEffortLow,
		models.ReasoningEffortHigh,
	}) {
		t.Fatalf("reasoning efforts = %#v", got.ReasoningEfforts)
	}
	if got.ReasoningDialect != reasoning.DialectBudget || got.ReasoningOffSupport != reasoning.OffSupportRejected {
		t.Fatalf("wire declarations = dialect %q, off %q", got.ReasoningDialect, got.ReasoningOffSupport)
	}
	if got.ReasoningDefaultOn == nil || *got.ReasoningDefaultOn {
		t.Fatalf("reasoning_default_on = %v, want false", got.ReasoningDefaultOn)
	}
	if got.ThinkingBudgetMin == nil || *got.ThinkingBudgetMin != 0 || got.ThinkingBudgetMax == nil || *got.ThinkingBudgetMax != 24576 {
		t.Fatalf("budget range = %v..%v, want 0..24576", got.ThinkingBudgetMin, got.ThinkingBudgetMax)
	}
}

func boolPointer(value bool) *bool { return &value }
func intPointer(value int) *int    { return &value }
