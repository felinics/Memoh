package models

import (
	"testing"

	googlegenerative "github.com/felinics/twilight/provider/google/generativeai"

	"github.com/felinics/memoh/internal/reasoning"
)

func intPtr(v int) *int { return &v }

// The two Gemini generations take mutually exclusive fields, and sending both is
// a 400. Which one applies comes from the model's declared dialect — never from
// its id — so these cases are the contract between the catalog and the wire.
func TestGoogleThinkingFollowsTheDeclaredDialect(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		cfg        SDKModelConfig
		wantSend   bool
		wantLevel  string
		wantBudget *int
	}{
		{
			// Rows imported before reasoning_dialect existed must retain the old
			// request shape until a trusted catalog refresh backfills the field.
			// Treating an empty declaration as the newest dialect sends
			// thinkingLevel to Gemini 2.5, which rejects the request.
			name: "legacy row without a dialect sends no control",
			cfg: SDKModelConfig{
				ReasoningConfig: &ReasoningConfig{Active: true, Effort: ReasoningEffortHigh},
			},
			wantSend: false,
		},
		{
			name: "3.x sends a named level",
			cfg: SDKModelConfig{
				ReasoningDialect: reasoning.DialectTier,
				ReasoningConfig:  &ReasoningConfig{Active: true, Effort: ReasoningEffortHigh},
			},
			wantSend:  true,
			wantLevel: ReasoningEffortHigh,
		},
		{
			name: "2.5 sends a budget inside its own range",
			cfg: SDKModelConfig{
				ReasoningDialect:  reasoning.DialectBudget,
				ThinkingBudgetMin: intPtr(128),
				ThinkingBudgetMax: intPtr(32768),
				ReasoningConfig:   &ReasoningConfig{Active: true, Effort: ReasoningEffortMedium},
			},
			wantSend: true,
			// medium sits halfway: 128 + 0.5*(32768-128)
			wantBudget: intPtr(16448),
		},
		{
			name: "a 2.5 model declaring off support can be turned off",
			cfg: SDKModelConfig{
				ReasoningDialect:    reasoning.DialectBudget,
				ReasoningOffSupport: reasoning.OffSupportAccepted,
				ThinkingBudgetMin:   intPtr(512),
				ThinkingBudgetMax:   intPtr(24576),
				ReasoningConfig:     &ReasoningConfig{Disabled: true},
			},
			wantSend:   true,
			wantBudget: intPtr(googlegenerative.ThinkingBudgetDisabled),
		},
		{
			name: "a 2.5 model declaring off rejection cannot be turned off",
			cfg: SDKModelConfig{
				ReasoningDialect:    reasoning.DialectBudget,
				ReasoningOffSupport: reasoning.OffSupportRejected,
				ThinkingBudgetMin:   intPtr(128),
				ThinkingBudgetMax:   intPtr(32768),
				ReasoningConfig:     &ReasoningConfig{Disabled: true},
			},
			wantSend: false,
		},
		{
			// The tier wire has no off value — minimal is the floor — so a stale
			// stored "off" sends nothing rather than a bogus level.
			name: "3.x disabled sends nothing",
			cfg: SDKModelConfig{
				ReasoningDialect: reasoning.DialectTier,
				ReasoningConfig:  &ReasoningConfig{Disabled: true},
			},
			wantSend: false,
		},
		{
			name: "active without a tier asks the model to choose",
			cfg: SDKModelConfig{
				ReasoningDialect:  reasoning.DialectBudget,
				ThinkingBudgetMin: intPtr(128),
				ThinkingBudgetMax: intPtr(32768),
				ReasoningConfig:   &ReasoningConfig{Active: true},
			},
			wantSend:   true,
			wantBudget: intPtr(googlegenerative.ThinkingBudgetDynamic),
		},
		{
			name:     "no decision sends nothing",
			cfg:      SDKModelConfig{ReasoningDialect: reasoning.DialectTier},
			wantSend: false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := googleThinkingFor(tt.cfg)
			if ok != tt.wantSend {
				t.Fatalf("send = %v, want %v (got %+v)", ok, tt.wantSend, got)
			}
			if !tt.wantSend {
				return
			}
			if got.ThinkingLevel != tt.wantLevel {
				t.Errorf("level = %q, want %q", got.ThinkingLevel, tt.wantLevel)
			}
			switch {
			case tt.wantBudget == nil && got.ThinkingBudget != nil:
				t.Errorf("budget = %d, want unset", *got.ThinkingBudget)
			case tt.wantBudget != nil && got.ThinkingBudget == nil:
				t.Errorf("budget unset, want %d", *tt.wantBudget)
			case tt.wantBudget != nil && *got.ThinkingBudget != *tt.wantBudget:
				t.Errorf("budget = %d, want %d", *got.ThinkingBudget, *tt.wantBudget)
			}
			// Never both: the API rejects a request carrying budget and level.
			if got.ThinkingBudget != nil && got.ThinkingLevel != "" {
				t.Errorf("sent both budget and level: %+v", got)
			}
		})
	}
}

// Without includeThoughts the API emits no thought parts, so the reasoning stream
// stays empty even though thinking ran and was billed.
func TestGoogleThinkingRequestsThoughtsWheneverThinkingIsOn(t *testing.T) {
	t.Parallel()

	on, ok := googleThinkingFor(SDKModelConfig{
		ReasoningDialect: reasoning.DialectTier,
		ReasoningConfig:  &ReasoningConfig{Active: true, Effort: ReasoningEffortLow},
	})
	if !ok || on.IncludeThoughts == nil || !*on.IncludeThoughts {
		t.Fatalf("thinking on should request thoughts: %+v", on)
	}

	off, ok := googleThinkingFor(SDKModelConfig{
		ReasoningDialect:    reasoning.DialectBudget,
		ReasoningOffSupport: reasoning.OffSupportAccepted,
		ThinkingBudgetMin:   intPtr(512),
		ThinkingBudgetMax:   intPtr(24576),
		ReasoningConfig:     &ReasoningConfig{Disabled: true},
	})
	if !ok {
		t.Fatal("a model declaring off support should send an explicit off budget")
	}
	if off.IncludeThoughts != nil {
		t.Errorf("thinking off should not ask for thoughts: %+v", off)
	}
}

// A budget tier scales with the model's own ceiling rather than a fixed table,
// because two Gemini models with different ranges cannot share token counts.
func TestGoogleBudgetScalesWithTheModelRange(t *testing.T) {
	t.Parallel()

	narrow, _ := googleThinkingFor(SDKModelConfig{
		ReasoningDialect:  reasoning.DialectBudget,
		ThinkingBudgetMin: intPtr(512),
		ThinkingBudgetMax: intPtr(24576),
		ReasoningConfig:   &ReasoningConfig{Active: true, Effort: ReasoningEffortHigh},
	})
	wide, _ := googleThinkingFor(SDKModelConfig{
		ReasoningDialect:  reasoning.DialectBudget,
		ThinkingBudgetMin: intPtr(128),
		ThinkingBudgetMax: intPtr(32768),
		ReasoningConfig:   &ReasoningConfig{Active: true, Effort: ReasoningEffortHigh},
	})

	if narrow.ThinkingBudget == nil || wide.ThinkingBudget == nil {
		t.Fatal("both should carry a budget")
	}
	if *narrow.ThinkingBudget >= *wide.ThinkingBudget {
		t.Errorf("high on a narrower range (%d) should be below high on a wider one (%d)",
			*narrow.ThinkingBudget, *wide.ThinkingBudget)
	}
	// Both must land inside their own bounds; a fixed table would overshoot the
	// narrower model.
	if *narrow.ThinkingBudget > 24576 || *wide.ThinkingBudget > 32768 {
		t.Errorf("budget escaped its range: narrow=%d wide=%d",
			*narrow.ThinkingBudget, *wide.ThinkingBudget)
	}
}
