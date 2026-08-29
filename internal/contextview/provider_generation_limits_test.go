package contextview

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/models"
)

type anthropicEnvelopeProbeProvider struct{ *envelopeProbeProvider }

func (anthropicEnvelopeProbeProvider) Name() string { return "anthropic-mock" }

func TestProviderContextBudgetPlanReservesResolvedGenerationLimits(t *testing.T) {
	t.Parallel()

	reasoning := &models.ReasoningConfig{Active: true, Adaptive: true, Effort: models.ReasoningEffortHigh}
	plan, err := providerContextBudgetPlan(context.Background(), agentpkg.RunConfig{
		Model:                  &sdk.Model{ID: "claude", Provider: anthropicEnvelopeProbeProvider{&envelopeProbeProvider{}}},
		ReasoningConfig:        reasoning,
		ContextBudgetMaxTokens: 200_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.OutputReserve != 32_000 || plan.OutputReserveResolution != models.GenerationLimitsProviderDefault {
		t.Fatalf("plan = %+v, want the Anthropic adaptive default reserved as provider_default", plan)
	}

	legacy, err := providerContextBudgetPlan(context.Background(), agentpkg.RunConfig{ContextBudgetMaxTokens: 200_000})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.OutputReserve != models.DefaultOutputReserveTokens || legacy.OutputReserveResolution != models.GenerationLimitsEstimated {
		t.Fatalf("model-less plan = %+v, want an estimated default reserve", legacy)
	}
}

func TestGenerateReservesExactlyTheMaxTokensItSends(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		provider  func(*envelopeProbeProvider) sdk.Provider
		reasoning *models.ReasoningConfig
		wantSent  bool
	}{
		{
			name:      "anthropic adaptive",
			provider:  func(p *envelopeProbeProvider) sdk.Provider { return anthropicEnvelopeProbeProvider{p} },
			reasoning: &models.ReasoningConfig{Active: true, Adaptive: true, Effort: models.ReasoningEffortHigh},
			wantSent:  true,
		},
		{
			name:      "anthropic legacy thinking",
			provider:  func(p *envelopeProbeProvider) sdk.Provider { return anthropicEnvelopeProbeProvider{p} },
			reasoning: &models.ReasoningConfig{Active: true, Effort: models.ReasoningEffortHigh},
			wantSent:  true,
		},
		{
			name:     "generic completions",
			provider: func(p *envelopeProbeProvider) sdk.Provider { return p },
			wantSent: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var seen sdk.GenerateParams
			probe := &envelopeProbeProvider{handler: func(_ int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
				seen = params
				return &sdk.GenerateResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
			}}
			agent := agentpkg.New(agentpkg.Deps{ContextViewApplier: ProviderRunConfigApplier(nil)})
			currentIndex := 0
			lifecycle := contextfrag.NewLifecycleHolder()
			_, err := agent.Generate(context.Background(), agentpkg.RunConfig{
				Model:                          &sdk.Model{ID: "model", Provider: tc.provider(probe), Type: sdk.ModelTypeChat},
				ReasoningConfig:                tc.reasoning,
				System:                         "you are helpful",
				Messages:                       []sdk.Message{sdk.UserMessage("hello")},
				ContextCurrentUserMessageIndex: &currentIndex,
				ContextBudgetMaxTokens:         200_000,
				Identity:                       agentpkg.SessionContext{BotID: "bot-1"},
				ContextMutations:               contextfrag.NewMutationLedger(),
				ContextLifecycle:               lifecycle,
			})
			if err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			snapshot, ok := lifecycle.Snapshot()
			if !ok || snapshot.BudgetPlan == nil {
				t.Fatal("lifecycle snapshot lost the budget plan")
			}
			limits := models.ResolveGenerationLimits(models.ClientType(models.ResolveClientType(&sdk.Model{Provider: tc.provider(probe)})), tc.reasoning, 200_000)
			if snapshot.BudgetPlan.OutputReserve != limits.MaxOutputTokens {
				t.Fatalf("OutputReserve = %d, want resolved %d", snapshot.BudgetPlan.OutputReserve, limits.MaxOutputTokens)
			}
			switch {
			case !tc.wantSent && seen.MaxTokens != nil:
				t.Fatalf("MaxTokens = %d, want the provider default to stand", *seen.MaxTokens)
			case tc.wantSent && (seen.MaxTokens == nil || *seen.MaxTokens != snapshot.BudgetPlan.OutputReserve):
				t.Fatalf("MaxTokens = %v, want exactly the reserved %d", seen.MaxTokens, snapshot.BudgetPlan.OutputReserve)
			}
		})
	}
}
