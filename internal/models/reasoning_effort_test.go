package models

import (
	"slices"
	"testing"
)

func TestNearestEffortToMedium(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		levels []string
		want   string
	}{
		{name: "medium itself wins", levels: []string{"low", "medium", "high"}, want: ReasoningEffortMedium},
		{name: "below medium picks the closest", levels: []string{"minimal", "low"}, want: ReasoningEffortLow},
		{name: "above medium picks the closest", levels: []string{"high", "max"}, want: ReasoningEffortHigh},
		{name: "tie breaks toward the weaker tier", levels: []string{"low", "high"}, want: ReasoningEffortLow},
		{
			// Registry order is arbitrary, so the result must come from tier
			// distance rather than from position in the input.
			name:   "strongest-first input still resolves by distance",
			levels: []string{"max", "high", "low", "none"},
			want:   ReasoningEffortLow,
		},
		{
			// "disable" is a settings sentinel, not a tier a model can advertise;
			// leaking it here would silently turn reasoning off.
			name:   "disable sentinel is not selectable",
			levels: []string{ReasoningEffortDisable, "high"},
			want:   ReasoningEffortHigh,
		},
		{name: "unknown values are ignored", levels: []string{"turbo", "low"}, want: ReasoningEffortLow},
		{name: "no usable tier returns empty", levels: []string{ReasoningEffortDisable, "turbo"}, want: ""},
		{name: "empty input returns empty", levels: nil, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := NearestEffortToMedium(tt.levels); got != tt.want {
				t.Fatalf("NearestEffortToMedium(%v) = %q, want %q", tt.levels, got, tt.want)
			}
		})
	}
}

func TestIsReasoningDisabled(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		effort string
		want   bool
	}{
		{effort: ReasoningEffortDisable, want: true},
		{effort: "  disable  ", want: true},
		// Legacy spelling: "none" was declarable and storable before off was
		// unified onto the disable token, so stored values must still read as off.
		{effort: ReasoningEffortNone, want: true},
		{effort: "", want: false},
		{effort: ReasoningEffortMinimal, want: false},
	} {
		if got := IsReasoningDisabled(tt.effort); got != tt.want {
			t.Errorf("IsReasoningDisabled(%q) = %v, want %v", tt.effort, got, tt.want)
		}
	}
}

// A model declares whether it can be turned off, and "disable" is how it says so.
// The OpenAI wire spelling of that state is not declarable — adaptors translate
// into it — so accepting both would give one state two selectable tokens again.
func TestIsValidReasoningEffortVocabulary(t *testing.T) {
	t.Parallel()

	if !IsValidReasoningEffort(ReasoningEffortDisable) {
		t.Error("IsValidReasoningEffort rejected disable, which a model must be able to advertise")
	}
	if IsValidReasoningEffort(ReasoningEffortNone) {
		t.Error("IsValidReasoningEffort accepted none, which is a provider wire value")
	}
}

// Declarations written before off was unified, and provider registries that have
// not been regenerated, still advertise the legacy spelling. Normalizing on both
// boundaries of ModelConfig is what keeps those models readable as turn-off
// capable — dropping the value instead would hide Off from a model that supports
// it, and would make every consumer that looks for the disable token misread it.
func TestNormalizeModelConfigRewritesLegacyOff(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "legacy spelling becomes the declarable token",
			in:   []string{ReasoningEffortNone, "low", "medium", "high"},
			want: []string{ReasoningEffortDisable, "low", "medium", "high"},
		},
		{
			name: "both spellings collapse to one entry",
			in:   []string{ReasoningEffortNone, ReasoningEffortDisable, "low"},
			want: []string{ReasoningEffortDisable, "low"},
		},
		{
			name: "active tiers are untouched",
			in:   []string{"minimal", "low", "high"},
			want: []string{"minimal", "low", "high"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeModelConfig(ModelConfig{ReasoningEfforts: tt.in}).ReasoningEfforts
			if !slices.Equal(got, tt.want) {
				t.Errorf("ReasoningEfforts = %v, want %v", got, tt.want)
			}
		})
	}
}

// Normalizing before validation is what lets a legacy declaration be stored at all:
// the vocabulary no longer accepts "none", so a config carrying it would otherwise
// be rejected on write.
func TestLegacyOffDeclarationSurvivesValidation(t *testing.T) {
	t.Parallel()

	m := Model{
		ModelID:    "m",
		ProviderID: "11111111-1111-1111-1111-111111111111",
		Type:       ModelTypeChat,
		Config:     normalizeModelConfig(ModelConfig{ReasoningEfforts: []string{ReasoningEffortNone, "high"}}),
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want a legacy declaration to normalize and pass", err)
	}
}

// The nearest-tier fallback must never resolve an active reasoning config to off,
// which is why the disable token is kept out of the tier ordering.
func TestNearestEffortToMediumNeverReturnsOff(t *testing.T) {
	t.Parallel()

	if got := NearestEffortToMedium([]string{ReasoningEffortDisable}); got != "" {
		t.Errorf("NearestEffortToMedium([disable]) = %q, want empty", got)
	}
	if got := NearestEffortToMedium([]string{ReasoningEffortDisable, ReasoningEffortHigh}); got != ReasoningEffortHigh {
		t.Errorf("NearestEffortToMedium([disable high]) = %q, want high", got)
	}
}

func TestLegacyGoogleRowIsSupportedWithoutAControl(t *testing.T) {
	t.Parallel()

	m := Model{
		ModelID: "gemini-2.5-pro",
		Type:    ModelTypeChat,
		Config: ModelConfig{
			Compatibilities:  []string{CompatReasoning},
			ReasoningEfforts: []string{ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh},
		},
	}
	opts := m.ReasoningOptions(string(ClientTypeGoogleGenerativeAI))
	if !opts.Supported || opts.CanDisable || len(opts.Efforts) != 0 || opts.DefaultEffort != "" {
		t.Fatalf("legacy Google options = %+v, want supported without controls", opts)
	}
}
