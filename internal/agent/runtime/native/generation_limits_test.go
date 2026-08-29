package native

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/models"
)

func TestRunConfigGenerationLimitsFollowModelAndReasoning(t *testing.T) {
	t.Parallel()

	anthropic := RunConfig{
		Model:                  &sdk.Model{ID: "claude", Provider: anthropicNameMockProvider{&atomicMockProvider{}}},
		ReasoningConfig:        &models.ReasoningConfig{Active: true, Adaptive: true, Effort: models.ReasoningEffortHigh},
		ContextBudgetMaxTokens: 200_000,
	}
	if got := anthropic.GenerationLimits(); got.MaxOutputTokens != 32_000 || !got.Requested || got.Resolution != models.GenerationLimitsProviderDefault {
		t.Fatalf("anthropic adaptive limits = %+v, want requested 32000 provider_default", got)
	}
	generic := RunConfig{Model: &sdk.Model{ID: "mock", Provider: &atomicMockProvider{}}, ContextBudgetMaxTokens: 128_000}
	if got := generic.GenerationLimits(); got.MaxOutputTokens != models.DefaultOutputReserveTokens || got.Requested {
		t.Fatalf("generic limits = %+v, want unrequested %d", got, models.DefaultOutputReserveTokens)
	}
	if got := (RunConfig{}).GenerationLimits(); got.MaxOutputTokens != models.DefaultOutputReserveTokens || got.Requested {
		t.Fatalf("model-less limits = %+v, want unrequested default", got)
	}
}

func TestAgentGenerateSendsMaxTokensOnlyWhenLimitsAreRequested(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		provider sdk.Provider
		want     *int
	}{
		{name: "anthropic requests the mirrored default", provider: anthropicNameMockProvider{&atomicMockProvider{}}, want: intPtr(4096)},
		{name: "generic completions keeps the provider default", provider: &atomicMockProvider{}, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var seen sdk.GenerateParams
			handler := func(_ int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
				seen = cloneGenerateParams(params)
				return &sdk.GenerateResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
			}
			switch p := tc.provider.(type) {
			case anthropicNameMockProvider:
				p.handler = handler
			case *atomicMockProvider:
				p.handler = handler
			}
			a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
				return cfg, nil
			}})
			plan := contextfrag.ContextBudgetPlan{Window: 200_000, OutputReserve: 4096}
			_, err := a.Generate(context.Background(), RunConfig{
				Model:                  &sdk.Model{ID: "model", Provider: tc.provider},
				System:                 "system",
				Messages:               []sdk.Message{sdk.UserMessage("task")},
				Identity:               SessionContext{BotID: "bot-1"},
				ContextMutations:       contextfrag.NewMutationLedger(),
				ContextBudgetMaxTokens: 200_000,
				ContextManifest:        contextfrag.Manifest{BudgetPlan: &plan},
			})
			if err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			switch {
			case tc.want == nil && seen.MaxTokens != nil:
				t.Fatalf("MaxTokens = %d, want the provider default to stand", *seen.MaxTokens)
			case tc.want != nil && (seen.MaxTokens == nil || *seen.MaxTokens != *tc.want):
				t.Fatalf("MaxTokens = %v, want %d", seen.MaxTokens, *tc.want)
			}
		})
	}
}

func intPtr(v int) *int { return &v }
