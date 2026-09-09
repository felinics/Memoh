package reasoning

import "testing"

// A model added outside our curated templates carries no thinking_mode and no
// effort tiers, so the empty-mode bridge is all that stands between it and the
// wrong wire shape. Claude 4.6+ rejects the legacy thinking wire with a hard
// 400, while the adaptive wire is merely inert on older models — so when the
// version is unreadable, the bridge guesses new.
func TestResolveModeInfersAdaptiveForUnknownClaude(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		// The live failure this exists for: a gateway-synced Claude with only
		// a "reasoning" compatibility flag.
		{"anthropic/claude-sonnet-5", ModeAdaptive},

		// Family-first ids, direct and prefixed.
		{"claude-opus-5", ModeAdaptive},
		{"claude-sonnet-4-6", ModeAdaptive},
		{"claude-sonnet-4.6", ModeAdaptive},
		{"claude-opus-4-8-fast", ModeAdaptive},
		{"anthropic/claude-opus-4.7", ModeAdaptive},
		{"us.anthropic.claude-opus-4-8-v1:0", ModeAdaptive},

		// A release date in the id is not a version: 4-20250514 is 4.x with the
		// date ignored, and plain Claude 4 predates adaptive.
		{"claude-opus-4-20250514", ModeToggle},

		// The frozen legacy families keep the legacy wire.
		{"claude-3-7-sonnet", ModeToggle},
		{"claude-3-5-haiku", ModeToggle},
		{"claude-3-haiku", ModeToggle},
		{"claude-2.1", ModeToggle},
		{"claude-instant-1.2", ModeToggle},
		{"claude-opus-4-5", ModeToggle},
		{"claude-sonnet-4", ModeToggle},

		// Recognizably Claude, version unreadable: guess new.
		{"claude-nextgen", ModeAdaptive},
		{"my-gateway/claude-custom", ModeAdaptive},

		// Not Claude at all: the old bridge rule stands. A compatibility
		// gateway hosting these speaks the older wire if it speaks any.
		{"deepseek-v4", ModeToggle},
		{"glm-4.6", ModeToggle},
		{"my-fast-model", ModeToggle},
	}

	for _, tc := range cases {
		if got := ResolveMode("", true, tc.id); got != tc.want {
			t.Errorf("ResolveMode(%q): got %s, want %s", tc.id, got, tc.want)
		}
	}
}

// A declared mode always wins: the classifier is a fallback, not an override.
// This is the escape hatch — a user can force either wire on any id.
func TestResolveModeDeclarationBeatsClassifier(t *testing.T) {
	if got := ResolveMode(ModeToggle, true, "claude-opus-5"); got != ModeToggle {
		t.Errorf("declared toggle on a 5.x id: got %s, want toggle", got)
	}
	if got := ResolveMode(ModeAdaptive, true, "claude-2.1"); got != ModeAdaptive {
		t.Errorf("declared adaptive on a 2.x id: got %s, want adaptive", got)
	}
	if got := ResolveMode(ModeNone, true, "claude-opus-5"); got != ModeNone {
		t.Errorf("declared none on a 5.x id: got %s, want none", got)
	}
}

// Without the reasoning compatibility flag there is nothing to infer: the
// model has no thinking concept, whatever its name looks like.
func TestResolveModeNoCompatMeansNone(t *testing.T) {
	if got := ResolveMode("", false, "claude-opus-5"); got != ModeNone {
		t.Errorf("no compat flag: got %s, want none", got)
	}
}

// The whole picker path for a bridge-resolved model: no declared tiers means
// the common base is offered, so the UI shows a real choice and the wire gets
// a valid output_config.effort.
func TestOptionsForBridgeResolvedAdaptiveModel(t *testing.T) {
	mode := ResolveMode("", true, "anthropic/claude-sonnet-5")
	if mode != ModeAdaptive {
		t.Fatalf("mode: got %s, want adaptive", mode)
	}
	opts := OptionsFor(mode, nil, "anthropic-messages", "")
	if !opts.Supported {
		t.Fatal("options: not supported, want a usable control")
	}
	if len(opts.Efforts) == 0 {
		t.Fatal("options: no effort tiers — the picker would render empty")
	}
	if opts.DefaultEffort != EffortMedium {
		t.Errorf("default effort: got %q, want %q", opts.DefaultEffort, EffortMedium)
	}
}

// A model resolved to adaptive purely by the id bridge advertises no effort
// tiers. In the live path effectiveEfforts supplies the low/medium/high base
// before pickEffort runs, but pickEffort must hold on its own too — every 4.6+
// generation accepts medium, so that is the floor even if a caller ever hands
// it a bare list.
func TestPickEffortFallsBackToMediumWithNoAdvertisedTiers(t *testing.T) {
	if got := pickEffort("", "", nil); got != EffortMedium {
		t.Errorf("no tiers, nothing stored: got %q, want %q", got, EffortMedium)
	}
	// A stored or requested tier the model never advertised must not leak
	// through just because the advertised list is empty.
	if got := pickEffort("xhigh", "", nil); got != EffortMedium {
		t.Errorf("unadvertised request on empty tiers: got %q, want %q", got, EffortMedium)
	}
	if got := pickEffort("", "max", nil); got != EffortMedium {
		t.Errorf("unadvertised stored tier on empty tiers: got %q, want %q", got, EffortMedium)
	}
}
