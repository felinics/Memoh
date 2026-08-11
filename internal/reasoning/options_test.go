package reasoning

import (
	"slices"
	"testing"
)

func TestOptionsForNeverOffersTheDisableTokenAsATier(t *testing.T) {
	t.Parallel()

	// The disable token travels in the advertised list because that is how a model
	// declares it can be turned off. It must not come back out as something a user
	// can pick as an *effort* — that confusion is what let an active config
	// resolve to "off" before this package existed.
	opts := OptionsFor(ModeToggle, []string{EffortDisable, EffortLow, EffortHigh}, "openai-completions")

	if !opts.CanDisable {
		t.Fatal("advertised disable token should report CanDisable")
	}
	if slices.Contains(opts.Efforts, EffortDisable) {
		t.Fatalf("disable leaked into the tier list: %v", opts.Efforts)
	}
	if slices.Contains(opts.Efforts, EffortNone) {
		t.Fatalf("the wire spelling of off leaked into the tier list: %v", opts.Efforts)
	}
}

func TestOptionsForOffReachability(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		mode       string
		advertised []string
		clientType string
		canDisable bool
	}{
		{
			// DeepSeek V4: off travels through the compat layer, and the catalog
			// says so. This is the case that was silently missing from the picker.
			name:       "declared disable is reachable",
			mode:       ModeToggle,
			advertised: []string{EffortDisable, EffortLow, EffortHigh},
			clientType: "openai-completions",
			canDisable: true,
		},
		{
			// Claude <=4.5: no catalog ever advertises the token for Anthropic
			// because the wire has no off value — absence of the field is off.
			name:       "anthropic toggle is off by omission",
			mode:       ModeToggle,
			advertised: []string{EffortLow, EffortMedium, EffortHigh},
			clientType: ClientTypeAnthropicMessages,
			canDisable: true,
		},
		{
			// Claude 4.6+: omitting the field leaves adaptive thinking in charge,
			// which still thinks. Off is not reachable by omission here.
			name:       "anthropic adaptive cannot be turned off by omission",
			mode:       ModeAdaptive,
			advertised: []string{EffortLow, EffortHigh, EffortMax},
			clientType: ClientTypeAnthropicMessages,
			canDisable: false,
		},
		{
			// gpt-5.0 advertises minimal but not none: it genuinely cannot be
			// turned off, and offering the control would be a dead switch.
			name:       "openai without the token cannot be turned off",
			mode:       ModeToggle,
			advertised: []string{EffortMinimal, EffortLow, EffortMedium},
			clientType: "openai-completions",
			canDisable: false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := OptionsFor(tt.mode, tt.advertised, tt.clientType).CanDisable; got != tt.canDisable {
				t.Fatalf("CanDisable = %v, want %v", got, tt.canDisable)
			}
		})
	}
}

func TestOptionsForUnsupportedModelOffersNothing(t *testing.T) {
	t.Parallel()

	opts := OptionsFor(ModeNone, []string{EffortLow}, "openai-completions")
	if opts.Supported || opts.CanDisable || len(opts.Efforts) > 0 || opts.DefaultEffort != "" {
		t.Fatalf("a model with no thinking concept should offer nothing, got %+v", opts)
	}
}

func TestOptionsForAppliesTheSameWirePolicyAsResolve(t *testing.T) {
	t.Parallel()

	// Generic OpenAI clients take xhigh as the ceiling. If the picker offered max
	// while the resolver filtered it, a user could select a tier that silently
	// became something else — the class of drift this package prevents.
	const advertisedMax = EffortMax
	advertised := []string{EffortLow, EffortHigh, advertisedMax}

	opts := OptionsFor(ModeToggle, advertised, "openai-completions")
	if slices.Contains(opts.Efforts, advertisedMax) {
		t.Fatalf("max should be filtered for generic OpenAI clients: %v", opts.Efforts)
	}

	cfg := ResolveConfig(ModeToggle, advertised, advertisedMax, "", "openai-completions")
	if cfg == nil || cfg.Effort == advertisedMax {
		t.Fatalf("resolver should not send a filtered tier, got %+v", cfg)
	}

	// Codex keeps max, and both answers must agree about that too.
	codexOpts := OptionsFor(ModeToggle, advertised, "openai-codex")
	if !slices.Contains(codexOpts.Efforts, advertisedMax) {
		t.Fatalf("codex should keep max: %v", codexOpts.Efforts)
	}
	codexCfg := ResolveConfig(ModeToggle, advertised, advertisedMax, "", "openai-codex")
	if codexCfg == nil || codexCfg.Effort != advertisedMax {
		t.Fatalf("codex resolver should send max, got %+v", codexCfg)
	}
}

func TestOptionsDefaultEffortMatchesWhatResolveWouldSend(t *testing.T) {
	t.Parallel()

	// The default a picker shows and the effort an unconfigured call sends are one
	// computation read twice. Any divergence here is the bug class this package
	// was built to make impossible.
	for _, advertised := range [][]string{
		{EffortLow, EffortMedium, EffortHigh},
		{EffortMinimal, EffortLow},
		{EffortHigh, EffortMax},
		{EffortDisable, EffortHigh, EffortXHigh},
		nil,
	} {
		opts := OptionsFor(ModeToggle, advertised, "openai-codex")
		cfg := ResolveConfig(ModeToggle, advertised, "", "", "openai-codex")
		if cfg == nil {
			t.Fatalf("advertised %v: resolver returned nil for a toggle model", advertised)
		}
		if opts.DefaultEffort != cfg.Effort {
			t.Fatalf("advertised %v: picker default %q != resolved effort %q",
				advertised, opts.DefaultEffort, cfg.Effort)
		}
	}
}

func TestReconcileStored(t *testing.T) {
	t.Parallel()

	canDisable := OptionsFor(ModeToggle, []string{EffortDisable, EffortLow, EffortHigh}, "openai-codex")
	cannotDisable := OptionsFor(ModeToggle, []string{EffortLow, EffortHigh}, "openai-codex")
	unsupported := OptionsFor(ModeNone, nil, "openai-codex")

	cases := []struct {
		name   string
		stored string
		opts   Options
		want   string
	}{
		{"a tier the model still offers survives", EffortHigh, canDisable, EffortHigh},
		{"off survives when off is reachable", EffortDisable, canDisable, EffortDisable},
		{"legacy off spelling survives as the current one", EffortNone, canDisable, EffortDisable},
		{"off falls back when off is not reachable", EffortDisable, cannotDisable, cannotDisable.DefaultEffort},
		{"a tier the model dropped falls back", EffortMinimal, cannotDisable, cannotDisable.DefaultEffort},
		{"an unset value takes the default", "", cannotDisable, cannotDisable.DefaultEffort},
		{"a model without thinking clears the value", EffortHigh, unsupported, ""},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ReconcileStored(tt.stored, tt.opts); got != tt.want {
				t.Fatalf("ReconcileStored(%q) = %q, want %q", tt.stored, got, tt.want)
			}
		})
	}
}
