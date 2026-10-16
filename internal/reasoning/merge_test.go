package reasoning

import (
	"slices"
	"testing"
)

func TestMergeTiers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		declared   []string
		discovered []string
		want       []string
	}{
		{
			name:       "silence keeps the declaration",
			declared:   []string{EffortDisable, EffortLow, EffortHigh, EffortMax},
			discovered: nil,
			want:       []string{EffortDisable, EffortLow, EffortHigh, EffortMax},
		},
		{
			name:       "silence keeps an undeclared list undeclared",
			declared:   nil,
			discovered: nil,
			want:       nil,
		},
		{
			name:       "a full answer replaces the declaration",
			declared:   []string{EffortMinimal, EffortDisable, EffortLow, EffortMedium, EffortHigh},
			discovered: []string{EffortDisable, EffortLow, EffortMedium, EffortHigh, EffortXHigh},
			want:       []string{EffortDisable, EffortLow, EffortMedium, EffortHigh, EffortXHigh},
		},
		{
			name:       "a shorter answer still replaces a tier it denies",
			declared:   []string{EffortMinimal, EffortLow, EffortMedium, EffortHigh},
			discovered: []string{EffortLow, EffortMedium, EffortHigh},
			want:       []string{EffortLow, EffortMedium, EffortHigh},
		},
		{
			name:       "an unexpressible capability token survives the replacement",
			declared:   []string{EffortDisable, EffortLow},
			discovered: []string{EffortLow, EffortHigh},
			want:       []string{EffortDisable, EffortLow, EffortHigh},
		},
		{
			name:       "the source's own copy of the token is not duplicated",
			declared:   []string{EffortDisable, EffortLow},
			discovered: []string{EffortDisable, EffortLow, EffortHigh},
			want:       []string{EffortDisable, EffortLow, EffortHigh},
		},
		{
			name:       "a capability token the declaration never had is not invented",
			declared:   []string{EffortLow, EffortMedium},
			discovered: []string{EffortLow, EffortMedium, EffortHigh},
			want:       []string{EffortLow, EffortMedium, EffortHigh},
		},
		{
			name:       "an empty answer is not silence",
			declared:   []string{EffortDisable, EffortLow, EffortHigh},
			discovered: []string{},
			want:       []string{EffortDisable},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := MergeTiers(tt.declared, tt.discovered); !slices.Equal(got, tt.want) {
				t.Fatalf("MergeTiers(%v, %v) = %v, want %v",
					tt.declared, tt.discovered, got, tt.want)
			}
		})
	}
}

// A model that advertises nothing must not acquire a tier list from a source that
// declined to answer, since the resolver supplies the common base for an empty
// declaration and a fabricated list here would override it.
func TestMergeTiersLeavesAnEmptyDeclarationAloneOnSilence(t *testing.T) {
	t.Parallel()

	got := MergeTiers(nil, nil)
	if got != nil {
		t.Fatalf("MergeTiers(nil, nil) = %v, want nil", got)
	}
}
