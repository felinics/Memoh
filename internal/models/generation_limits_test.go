package models

import "testing"

func TestResolveGenerationLimitsMirrorsAnthropicProviderDefaults(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		reasoning *ReasoningConfig
		want      int
	}{
		{name: "no reasoning config", reasoning: nil, want: 4096},
		{name: "explicitly disabled", reasoning: &ReasoningConfig{Disabled: true}, want: 4096},
		{name: "adaptive", reasoning: &ReasoningConfig{Active: true, Adaptive: true, Effort: ReasoningEffortHigh}, want: 32000},
		{name: "legacy low", reasoning: &ReasoningConfig{Active: true, Effort: ReasoningEffortLow}, want: 4096 + 5000},
		{name: "legacy medium", reasoning: &ReasoningConfig{Active: true, Effort: ReasoningEffortMedium}, want: 4096 + 16000},
		{name: "legacy high", reasoning: &ReasoningConfig{Active: true, Effort: ReasoningEffortHigh}, want: 4096 + 50000},
		{name: "legacy unknown effort defaults to medium", reasoning: &ReasoningConfig{Active: true}, want: 4096 + 16000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveGenerationLimits(ClientTypeAnthropicMessages, tc.reasoning, 200_000)
			if got.MaxOutputTokens != tc.want {
				t.Fatalf("MaxOutputTokens = %d, want %d", got.MaxOutputTokens, tc.want)
			}
			if !got.Requested {
				t.Fatal("Anthropic limits must be sent as max_tokens so the plan and the request share one value")
			}
			if got.Resolution != GenerationLimitsProviderDefault {
				t.Fatalf("Resolution = %q, want %q", got.Resolution, GenerationLimitsProviderDefault)
			}
		})
	}
}

func TestResolveGenerationLimitsFitsAnthropicThinkingIntoHalfTheWindow(t *testing.T) {
	t.Parallel()

	legacy := ResolveGenerationLimits(ClientTypeAnthropicMessages, &ReasoningConfig{Active: true, Effort: ReasoningEffortHigh}, 64_000)
	if legacy.MaxOutputTokens != 32_000 || !legacy.Requested || legacy.Resolution != GenerationLimitsWindowClamped {
		t.Fatalf("legacy high on 64k = %+v, want 4096 + a 27904 budget fitted to half the window", legacy)
	}
	if budget := AnthropicThinkingBudget(ReasoningEffortHigh, 64_000); budget != 27_904 {
		t.Fatalf("fitted budget = %d, want 27904 so budget_tokens stays below max_tokens", budget)
	}
	if budget := AnthropicThinkingBudget(ReasoningEffortHigh, 200_000); budget != 50_000 {
		t.Fatalf("budget on 200k = %d, want the full 50000", budget)
	}
	if budget := AnthropicThinkingBudget(ReasoningEffortHigh, 8_000); budget != 1_024 {
		t.Fatalf("budget on 8k = %d, want the Anthropic minimum", budget)
	}
	adaptive := ResolveGenerationLimits(ClientTypeAnthropicMessages, &ReasoningConfig{Active: true, Adaptive: true}, 100_000)
	if adaptive.MaxOutputTokens != 32_000 || adaptive.Resolution != GenerationLimitsProviderDefault {
		t.Fatalf("adaptive on 100k = %+v, want the untouched 32000 default", adaptive)
	}
	small := ResolveGenerationLimits(ClientTypeAnthropicMessages, &ReasoningConfig{Active: true, Adaptive: true}, 60_000)
	if small.MaxOutputTokens != 30_000 || !small.Requested || small.Resolution != GenerationLimitsWindowClamped {
		t.Fatalf("adaptive on 60k = %+v, want 30000 window_clamped", small)
	}
}

func TestResolveGenerationLimitsEstimatesWithoutRequestingForOtherClients(t *testing.T) {
	t.Parallel()

	for _, clientType := range []ClientType{ClientTypeOpenAIResponses, ClientTypeOpenAICompletions, ClientTypeGoogleGenerativeAI, ClientTypeGitHubCopilot} {
		t.Run(string(clientType), func(t *testing.T) {
			t.Parallel()
			plain := ResolveGenerationLimits(clientType, nil, 128_000)
			if plain.MaxOutputTokens != DefaultOutputReserveTokens || plain.Requested || plain.Resolution != GenerationLimitsEstimated {
				t.Fatalf("plain limits = %+v, want unrequested estimate %d", plain, DefaultOutputReserveTokens)
			}
			reasoning := ResolveGenerationLimits(clientType, &ReasoningConfig{Active: true}, 128_000)
			if reasoning.MaxOutputTokens != DefaultReasoningOutputReserveTokens || reasoning.Requested {
				t.Fatalf("reasoning limits = %+v, want unrequested estimate %d", reasoning, DefaultReasoningOutputReserveTokens)
			}
		})
	}
}

func TestResolveGenerationLimitsMarksCodexAsIgnoringTheCap(t *testing.T) {
	t.Parallel()

	got := ResolveGenerationLimits(ClientTypeOpenAICodex, &ReasoningConfig{Active: true, Effort: ReasoningEffortHigh}, 400_000)
	if got.Requested || got.Resolution != GenerationLimitsProviderIgnores || got.MaxOutputTokens != DefaultReasoningOutputReserveTokens {
		t.Fatalf("codex limits = %+v, want unrequested reserve %d with provider_ignores", got, DefaultReasoningOutputReserveTokens)
	}
}

func TestResolveGenerationLimitsClampsEstimatesToAQuarterOfTheWindow(t *testing.T) {
	t.Parallel()

	got := ResolveGenerationLimits(ClientTypeOpenAICompletions, nil, 8_192)
	if got.MaxOutputTokens != 2_048 || got.Resolution != GenerationLimitsWindowClamped || got.Requested {
		t.Fatalf("small-window limits = %+v, want 2048 window_clamped", got)
	}
	if got := ResolveGenerationLimits(ClientTypeOpenAICompletions, nil, 32_767); got.MaxOutputTokens != 8_191 {
		t.Fatalf("quarter-window clamp = %+v, want 8191", got)
	}
	if got := ResolveGenerationLimits(ClientTypeOpenAICompletions, nil, 0); got.MaxOutputTokens != DefaultOutputReserveTokens {
		t.Fatalf("unknown window must not clamp: %+v", got)
	}
}
