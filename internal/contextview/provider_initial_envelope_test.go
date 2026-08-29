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
)

// floorPricedHistory mirrors the application's history estimates, which are
// the legacy bytes/4 floor rather than the provider envelope estimate.
func floorPricedHistory(count, bytesEach int) []contextfrag.ContextFrag {
	frags := make([]contextfrag.ContextFrag, 0, count)
	for i := range count {
		text := strings.Repeat(string(rune('a'+i%26)), bytesEach)
		msg := sdk.UserMessage(text)
		if i%2 == 1 {
			msg = sdk.AssistantMessage(text)
		}
		frag := historyMessageFrag(fmt.Sprintf("h%02d", i), msg)
		frag.TokenEstimate = contextfrag.TokensFromBytes(bytesEach)
		frags = append(frags, frag)
	}
	return frags
}

func TestApplyProviderRunConfigTrimsFloorPricedHistoryToTheRenderedEnvelope(t *testing.T) {
	t.Parallel()

	frags := append(
		[]contextfrag.ContextFrag{systemTextFrag("system", strings.Repeat("s", 400), contextfrag.KindSystemPrompt, 100)},
		floorPricedHistory(60, 1_000)...,
	)
	frags = append(frags, currentMessageFrag("current", "latest question"))
	zero := 0
	cfg := agentpkg.RunConfig{
		ContextSourceFrags:         frags,
		ContextBudgetMaxTokens:     16_000,
		ContextRecentProtectTokens: &zero,
	}

	out, err := ProviderRunConfigApplier(nil)(context.Background(), cfg)
	if err != nil {
		t.Fatalf("ApplyProviderRunConfig() error = %v, want history trimmed until the rendered envelope fits", err)
	}
	plan := out.ContextManifest.BudgetPlan
	if plan == nil {
		t.Fatal("budget plan missing from manifest")
	}
	rendered := contextfrag.ProviderEnvelopeTokens(out.System, out.Messages, nil)
	if rendered+plan.OutputReserve > plan.Window {
		t.Fatalf("rendered envelope = %d + reserve %d exceeds window %d", rendered, plan.OutputReserve, plan.Window)
	}
	if len(out.Messages) < 2 || len(out.Messages) >= 61 {
		t.Fatalf("messages = %d, want a trimmed history that still keeps recent turns", len(out.Messages))
	}
	if slack, next := plan.Window-plan.OutputReserve-rendered, contextfrag.ProviderEnvelopeTokens("", []sdk.Message{sdk.UserMessage(strings.Repeat("a", 1_000))}, nil); slack >= next {
		t.Fatalf("slack = %d tokens, want less than the next trimmed message (%d) so trimming stays tight", slack, next)
	}
	for _, record := range out.ContextMutations.Records() {
		if record.Kind == contextfrag.MutationContextBudgetFailure || record.Kind == contextfrag.MutationContextViewFallback {
			t.Fatalf("trimmable history recorded %+v", record)
		}
	}
}

// TestApplyProviderRunConfigFailsClosedWhenRenderingOutgrowsSelection keeps
// the rendered-envelope check as the last line of defense: a fragment that
// renders as many tiny messages is charged once by selection but per message
// by the rendered check, and that drift must still fail closed.
func TestApplyProviderRunConfigFailsClosedWhenRenderingOutgrowsSelection(t *testing.T) {
	t.Parallel()

	tiny := sdk.UserMessage("x")
	burst := historyMessageFrag("burst", tiny)
	for range 39 {
		burst.Parts = append(burst.Parts, contextfrag.Part{Type: contextfrag.PartSDKMessage, SDKMessage: &tiny})
	}
	frags := []contextfrag.ContextFrag{
		systemTextFrag("system", strings.Repeat("s", 2_784), contextfrag.KindSystemPrompt, 100),
		burst,
		currentMessageFrag("current", "current"),
	}
	out, err := ProviderRunConfigApplier(nil)(context.Background(), agentpkg.RunConfig{
		ContextSourceFrags:     frags,
		ContextBudgetMaxTokens: 1_200,
	})
	if !errors.Is(err, contextfrag.ErrBudgetUnsatisfied) {
		t.Fatalf("preflight error = %v, want rendered-envelope ErrBudgetUnsatisfied", err)
	}
	if !strings.Contains(err.Error(), "rendered_input=") {
		t.Fatalf("preflight error = %v, want the rendered-envelope detail", err)
	}
	records := out.ContextMutations.Records()
	if len(records) != 1 || records[0] != (contextfrag.MutationRecord{
		Kind: contextfrag.MutationContextBudgetFailure, Detail: "budget_unsatisfied",
	}) {
		t.Fatalf("rendered-envelope mutations = %#v, want one budget_unsatisfied record", records)
	}
}
