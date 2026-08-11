package reasoning

import "testing"

// Moved verbatim from internal/agent/application when the resolver became a leaf
// package; the assertions are unchanged.

func TestOffEffortFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		levels []string
		want   string
	}{
		{
			"declaring disable yields the OpenAI wire spelling of off",
			[]string{EffortDisable, "low", "medium"},
			EffortNone,
		},
		{
			// minimal reduces reasoning, it does not stop it, so standing in for off
			// would make Off and the Minimal tier the same request. Upstream also
			// never ships both: minimal is gpt-5.0's weakest tier and gpt-5.1
			// replaced it with none.
			"minimal is a tier, not a stand-in for off",
			[]string{EffortMinimal, "low", "medium"},
			"",
		},
		{"empty when only real tiers (omit, do not enable)", []string{"medium", "high", "xhigh"}, ""},
		{"legacy base yields empty (omit reasoning_effort)", []string{"low", "medium", "high"}, ""},
		{"empty levels yield empty", nil, ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := offEffortFor(tt.levels); got != tt.want {
				t.Fatalf("offEffortFor(%v) = %q, want %q", tt.levels, got, tt.want)
			}
		})
	}
}
