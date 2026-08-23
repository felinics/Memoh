package native

import (
	"strings"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
)

func TestRemainingStepBudgetUsesConservativeProviderEstimator(t *testing.T) {
	t.Parallel()

	params := &sdk.GenerateParams{
		System:   strings.Repeat("s", 101),
		Messages: []sdk.Message{sdk.UserMessage(strings.Repeat("m", 103))},
	}
	_, payloadBytes := contextfrag.ProviderPayloadHashAndBytes(params.System, params.Messages, params.Tools)
	legacy := contextfrag.TokensFromBytes(payloadBytes)
	conservative := contextfrag.ProviderBudgetTokensFromBytes(payloadBytes)
	if conservative <= legacy {
		t.Fatalf("fixture estimates = legacy %d conservative %d, want conservative to be greater", legacy, conservative)
	}

	const allowance = 1000
	if got, want := remainingStepBudget(allowance, params, len(params.Messages)), allowance-conservative; got != want {
		t.Fatalf("remainingStepBudget() = %d, want provider allowance %d", got, want)
	}
}
