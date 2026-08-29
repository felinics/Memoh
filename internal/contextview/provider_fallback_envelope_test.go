package contextview

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/models"
)

// buildErrorFragsFirstConfig reproduces an internal build error (duplicate
// fragment IDs) so the applier takes the legacy fallback path.
func buildErrorFragsFirstConfig(messages []sdk.Message, window int) agentpkg.RunConfig {
	system := systemTextFrag("system.prompt", "fragment system", contextfrag.KindSystemPrompt, 100)
	first := attentionMessageFrag("duplicate", sdk.UserMessage("first"), 10)
	second := attentionMessageFrag("duplicate", sdk.AssistantMessage("second"), 10)
	return agentpkg.RunConfig{
		System:                 "legacy system",
		Messages:               messages,
		ContextSourceFrags:     []contextfrag.ContextFrag{system, first, second},
		ContextBudgetMaxTokens: window,
	}
}

func TestApplyProviderRunConfigFallbackCarriesBudgetPlanAndStepReselector(t *testing.T) {
	t.Parallel()

	out, err := ProviderRunConfigApplier(nil)(context.Background(), buildErrorFragsFirstConfig([]sdk.Message{sdk.UserMessage("legacy message")}, 100_000))
	if err != nil {
		t.Fatalf("ApplyProviderRunConfig() error = %v, want ordinary build fallback", err)
	}
	plan := out.ContextManifest.BudgetPlan
	if plan == nil {
		t.Fatal("fallback manifest lost the budget plan")
	}
	limits := models.ResolveGenerationLimits(models.ClientTypeOpenAICompletions, nil, 100_000)
	if plan.Window != 100_000 || plan.OutputReserve != limits.MaxOutputTokens || plan.OutputReserveResolution != limits.Resolution {
		t.Fatalf("fallback plan = %+v, want window 100000 reserving %d (%s)", plan, limits.MaxOutputTokens, limits.Resolution)
	}
	if plan.ActualSystemCost != 0 || plan.HistoryBudget != 0 {
		t.Fatalf("fallback plan = %+v, want the failed build's system and history figures cleared", plan)
	}
	if out.ContextStepReselector == nil {
		t.Fatal("fallback assembly must keep step reselection; it is an assembly path, not a budget-disabled path")
	}
	if out.ContextMutations == nil {
		t.Fatal("fallback lost the mutation ledger")
	}
	records := out.ContextMutations.Records()
	if len(records) != 1 || records[0].Kind != contextfrag.MutationContextViewFallback || records[0].Detail != "build_error" {
		t.Fatalf("fallback records = %#v, want one build_error context fallback", records)
	}
}

func TestFallbackDispatchFailsClosedWhenLegacyPayloadExceedsAllowance(t *testing.T) {
	t.Parallel()

	provider := &envelopeProbeProvider{handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
		return nil, fmt.Errorf("provider must not be called for an oversized fallback payload (call %d)", call)
	}}
	agent := agentpkg.New(agentpkg.Deps{ContextViewApplier: ProviderRunConfigApplier(nil)})
	const window = 2_000
	limits := models.ResolveGenerationLimits(models.ClientTypeOpenAICompletions, nil, window)
	cfg := buildErrorFragsFirstConfig([]sdk.Message{sdk.UserMessage(strings.Repeat("o", 5_100))}, window)
	if cost := contextfrag.ProviderEnvelopeTokens(cfg.System, cfg.Messages, nil); cost <= window-limits.MaxOutputTokens || cost >= window {
		t.Fatalf("legacy payload costs %d tokens, want strictly between allowance %d and window %d", cost, window-limits.MaxOutputTokens, window)
	}
	cfg.Model = &sdk.Model{ID: "model", Provider: provider, Type: sdk.ModelTypeChat}
	cfg.Identity = agentpkg.SessionContext{BotID: "bot-1"}
	cfg.ContextMutations = contextfrag.NewMutationLedger()
	cfg.ContextLifecycle = contextfrag.NewLifecycleHolder()

	_, err := agent.Generate(context.Background(), cfg)
	if !errors.Is(err, contextfrag.ErrBudgetUnsatisfied) {
		t.Fatalf("Generate() error = %v, want %v", err, contextfrag.ErrBudgetUnsatisfied)
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("provider calls = %d, want 0", calls)
	}
	records := cfg.ContextMutations.Records()
	if !hasMutation(records, contextfrag.MutationContextViewFallback) || !hasMutation(records, contextfrag.MutationContextBudgetFailure) {
		t.Fatalf("mutations = %+v, want both the fallback and the budget failure recorded", records)
	}
}
