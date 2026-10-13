package models

import (
	"testing"

	"github.com/felinics/memoh/internal/reasoning"
)

// An explicit thinking{type:"disabled"} is sent wherever the catalog declares
// the model accepts it. Omitting the field is off only through Claude 4.8; from
// Opus 5 on, an omitted field leaves thinking running, so a declared off must
// travel on the wire whatever the model's default.
func TestAnthropicAcceptsExplicitOff(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		offSupport string
		want       bool
	}{
		{
			// Opus 5: accepts a disable at high effort and below.
			name:       "low effort only sends the explicit shape",
			offSupport: reasoning.OffSupportLowEffortOnly, want: true,
		},
		{
			// Claude 4.6 and Sonnet 5: accept a disable at any effort. On 4.6 it
			// equals omission, so sending it is harmless.
			name:       "accepted sends the explicit shape",
			offSupport: reasoning.OffSupportAccepted, want: true,
		},
		{
			// Fable 5: 400s on an explicit disable. Off is unreachable; the
			// picker keeps the control out.
			name:       "rejected cannot be turned off",
			offSupport: reasoning.OffSupportRejected, want: false,
		},
		{
			name:       "an undeclared model keeps the omission behaviour",
			offSupport: "", want: false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := anthropicAcceptsExplicitOff(tt.offSupport); got != tt.want {
				t.Fatalf("anthropicAcceptsExplicitOff(%q) = %v, want %v", tt.offSupport, got, tt.want)
			}
		})
	}
}
