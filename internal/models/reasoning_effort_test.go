package models

import "testing"

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
		{effort: ReasoningEffortNone, want: false},
		{effort: "", want: false},
	} {
		if got := IsReasoningDisabled(tt.effort); got != tt.want {
			t.Errorf("IsReasoningDisabled(%q) = %v, want %v", tt.effort, got, tt.want)
		}
	}
}

func TestIsValidReasoningEffortRejectsDisable(t *testing.T) {
	t.Parallel()

	// ModelConfig stores wire tiers only; "disable" is a settings-level value and
	// must never be persisted as a model capability.
	if IsValidReasoningEffort(ReasoningEffortDisable) {
		t.Fatal("IsValidReasoningEffort accepted the disable sentinel")
	}
}
