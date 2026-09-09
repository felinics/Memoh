package native

import (
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestRemainingStepBudgetUsesConservativeProviderEstimator(t *testing.T) {
	t.Parallel()

	params := &sdk.GenerateParams{
		System:   strings.Repeat("s", 101),
		Messages: []sdk.Message{sdk.UserMessage(strings.Repeat("m", 103))},
	}
	legacy := contextfrag.TokensFromBytes(len(params.System)) + contextfrag.EstimateSDKMessageTokens(params.Messages[0])
	if legacy != 50 {
		t.Fatalf("legacy estimate = %d, want 50", legacy)
	}

	const allowance = 1000
	if got := remainingStepBudget(allowance, params, len(params.Messages)); got != 936 {
		t.Fatalf("remainingStepBudget() = %d, want 936 (ceil(101/4)*1.25 + ceil(103/4)*1.25 = 64 reserved)", got)
	}
}

func TestStepEnvelopeEstimatesPriceFrozenPrefixImagesFlat(t *testing.T) {
	t.Parallel()

	photo := sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{
		sdk.TextPart{Text: "what is in this photo?"},
		sdk.ImagePart{Image: "data:image/jpeg;base64," + strings.Repeat("A", 400_000), MediaType: "image/jpeg"},
	}}
	params := &sdk.GenerateParams{
		System:   "system",
		Messages: []sdk.Message{photo, sdk.AssistantMessage("looking")},
	}
	const allowance = 128_000 - 8_192
	prefixCost := contextfrag.ProviderEnvelopeTokens(params.System, params.Messages[:1], nil)
	if prefixCost != 1_509 {
		t.Fatalf("photo prefix cost = %d, want 1509 (flat image + 9 text tokens)", prefixCost)
	}
	if got, want := remainingStepBudget(allowance, params, 1), allowance-prefixCost; got != want {
		t.Fatalf("remainingStepBudget() = %d, want %d", got, want)
	}
	if overflow := providerAttemptEnvelopeOverflow(params, allowance); overflow >= 0 {
		t.Fatalf("providerAttemptEnvelopeOverflow() = %d, want a photo turn to stay inside the allowance", overflow)
	}
}
