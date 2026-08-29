package contextview

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
)

func TestApplyProviderContextViewKeepsMaterializedCurrentUserUnderBudgetPressure(t *testing.T) {
	t.Parallel()

	currentUserIndex := 3
	memoryIndex := 1
	zero := 0
	cfg := agentpkg.RunConfig{
		Messages: []sdk.Message{
			sdk.AssistantMessage("old answer"),
			sdk.UserMessage("remembered fact"),
			sdk.UserMessage("workspace hook guidance"),
			sdk.UserMessage("pipeline current question"),
		},
		ContextCurrentUserMessageIndex: &currentUserIndex,
		ContextMemoryMessageIndex:      &memoryIndex,
		ContextHistoryTokenEstimates:   []int{200, 20, 20, 100},
		ContextTrimmableMessages:       1,
		ContextRecentProtectTokens:     &zero,
	}
	activateHistoryBudget(&cfg, 100)
	out, err := ProviderRunConfigApplier(nil)(context.Background(), cfg)
	if err != nil {
		t.Fatalf("ApplyProviderRunConfig() error = %v", err)
	}

	if len(out.Messages) == 0 || messageText(t, out.Messages[len(out.Messages)-1]) != "pipeline current question" {
		t.Fatalf("messages = %#v, materialized current user must survive as the trailing request", out.Messages)
	}
}

func activateHistoryBudget(cfg *agentpkg.RunConfig, legacyBudget int) {
	inputBudget, scale := activeHistoryInputBudget(legacyBudget)
	for i := range cfg.ContextHistoryTokenEstimates {
		cfg.ContextHistoryTokenEstimates[i] *= scale
	}
	for i := range cfg.ContextSourceFrags {
		cfg.ContextSourceFrags[i].TokenEstimate *= scale
	}
	frags := cfg.ContextSourceFrags
	if len(frags) == 0 {
		frags = CollectNonSystemProviderSourceFrags(context.Background(), *cfg)
	}
	tagged := tagFragments(frags, (&FragmentSelector{}).ProfileFor(contextfrag.IntentRunConfigPreProvider))
	for i := range tagged {
		tagged[i].Tokens = contextfrag.ResolveProviderBudgetFragTokens(tagged[i].Frag)
	}
	inputBudget += protectedHistoryTokenCost(tagged)
	currentRequestCost, err := providerCurrentRequestCost(context.Background(), *cfg)
	if err != nil {
		panic(err)
	}
	toolDefsCost := 0
	for _, def := range cfg.ContextToolDefs {
		toolDefsCost += def.TokenEstimate
	}
	inputBudget += currentRequestCost + toolDefsCost
	cfg.ContextBudgetMaxTokens = contextWindowForDefaultOutputReserve(inputBudget)
}

func activeHistoryInputBudget(legacyBudget int) (inputBudget, scale int) {
	if legacyBudget <= 0 {
		return 0, 1
	}
	scale = 2
	if scaled := legacyBudget * scale; scaled < MinimumSystemBudgetTokens {
		scale = (MinimumSystemBudgetTokens + legacyBudget - 1) / legacyBudget
	}
	noticeCost := contextfrag.ResolveProviderBudgetFragTokens(TrimNoticeFrag(contextfrag.Scope{}))
	return legacyBudget*scale + noticeCost, scale
}
