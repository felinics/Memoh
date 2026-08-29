package models

import (
	"testing"

	"github.com/felinics/memoh/internal/reasoning"
)

// Two independent facts decide whether a disabled call must say so on the wire:
// whether the model accepts thinking{type:"disabled"}, and whether omitting the
// field would have worked anyway. Conflating them is how a user's "off" on Opus 5
// would silently keep thinking — billed as output tokens and counted against
// max_tokens.
func TestAnthropicExplicitOffNeedsBothFacts(t *testing.T) {
	t.Parallel()

	on, off := true, false

	cases := []struct {
		name       string
		offSupport string
		defaultOn  *bool
		want       bool
	}{
		{
			// Opus 5: thinks by default and accepts a disable — the case that needs
			// the explicit shape.
			name:       "on by default and accepting needs the explicit shape",
			offSupport: reasoning.OffSupportLowEffortOnly, defaultOn: &on, want: true,
		},
		{
			name:       "so does an unconditionally accepting model that is on by default",
			offSupport: reasoning.OffSupportAccepted, defaultOn: &on, want: true,
		},
		{
			// Claude 4.6: accepts a disable, but omitting already means off, so
			// sending it would be noise.
			name:       "off by default needs nothing, even though it accepts one",
			offSupport: reasoning.OffSupportAccepted, defaultOn: &off, want: false,
		},
		{
			// Fable 5: thinks by default and 400s on an explicit disable. Off is
			// unreachable; the picker keeps the control out.
			name:       "on by default but rejecting cannot be turned off at all",
			offSupport: reasoning.OffSupportRejected, defaultOn: &on, want: false,
		},
		{
			name:       "an undeclared model keeps the omission behaviour",
			offSupport: "", defaultOn: nil, want: false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := anthropicNeedsExplicitOff(tt.offSupport, tt.defaultOn); got != tt.want {
				t.Fatalf("needsExplicitOff(%q, %v) = %v, want %v",
					tt.offSupport, tt.defaultOn, got, tt.want)
			}
		})
	}
}
