package models

import (
	"testing"

	"github.com/memohai/memoh/internal/reasoning"
)

// Omission is not a way to turn thinking off from Opus 5 on: those models think by
// default, so an omitted field leaves thinking running — billed as output tokens
// and counted against max_tokens — while the user believes it is off. Where the
// model accepts it, the adaptor must say so explicitly.
func TestAnthropicExplicitOffIsSentWhereAccepted(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		offSupport string
		want       bool
	}{
		{"a model that accepts an explicit disable gets one", reasoning.OffSupportAccepted, true},
		{"so does one that accepts it below xhigh", reasoning.OffSupportLowEffortOnly, true},
		{"a model that rejects it gets an omitted field instead", reasoning.OffSupportRejected, false},
		{"an undeclared model keeps the omission behaviour", "", false},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := anthropicAcceptsExplicitOff(tt.offSupport); got != tt.want {
				t.Fatalf("acceptsExplicitOff(%q) = %v, want %v", tt.offSupport, got, tt.want)
			}
		})
	}
}
