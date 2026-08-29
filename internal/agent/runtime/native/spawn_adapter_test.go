package native

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	tools "github.com/felinics/memoh/internal/agent/tool"
)

func TestSpawnRunConfigClassifiesRawQueryAsCurrentUser(t *testing.T) {
	t.Parallel()
	const query = "  keep raw query bytes  "
	rc := runConfigFromSpawnRunConfig(tools.SpawnRunConfig{
		System: SpawnSystemPrompt(sessionmode.Subagent), Query: query, SessionType: sessionmode.Subagent,
		Messages: []sdk.Message{sdk.UserMessage("history")},
	})

	if rc.ContextCurrentUserMessageIndex == nil || *rc.ContextCurrentUserMessageIndex != 1 {
		t.Fatalf("current user index = %#v, want 1", rc.ContextCurrentUserMessageIndex)
	}
	if text, _ := rc.Messages[1].Content[0].(sdk.TextPart); text.Text != query {
		t.Fatalf("spawn query = %q, want byte-exact %q", text.Text, query)
	}
	current := 0
	for _, frag := range rc.ContextSourceFrags {
		if frag.Slot == contextfrag.SlotCurrentUser {
			current++
			if frag.Kind != contextfrag.KindCurrentUserMessage {
				t.Fatalf("current fragment kind = %q", frag.Kind)
			}
		}
	}
	if current != 1 {
		t.Fatalf("current fragment count = %d, want 1", current)
	}
}

func TestSpawnRunConfigCarriesContextBudgetAndToolExchangePolicy(t *testing.T) {
	t.Parallel()

	policy := &contextfrag.ToolExchangePolicy{MinMessages: 10}
	rc := runConfigFromSpawnRunConfig(tools.SpawnRunConfig{
		ContextBudgetMaxTokens:    128000,
		ContextToolExchangePolicy: policy,
	})

	if rc.ContextBudgetMaxTokens != 128000 {
		t.Fatalf("ContextBudgetMaxTokens = %d, want 128000", rc.ContextBudgetMaxTokens)
	}
	if rc.ContextToolExchangePolicy != policy {
		t.Fatalf("ContextToolExchangePolicy = %p, want same pointer %p", rc.ContextToolExchangePolicy, policy)
	}
}

func TestSpawnAdapterGenerateWithWatchdogFailsOnStreamError(t *testing.T) {
	t.Parallel()

	a := New(Deps{
		ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
			return cfg, contextfrag.ErrBudgetUnsatisfied
		},
	})
	adapter := NewSpawnAdapter(a)

	result, err := adapter.GenerateWithWatchdog(context.Background(), tools.SpawnRunConfig{
		Model: &sdk.Model{
			ID:       "spawn-preflight-error",
			Provider: &preflightCountingProvider{},
			Type:     sdk.ModelTypeChat,
		},
		Query: "do the task",
	}, func() {})

	if result != nil {
		t.Fatalf("GenerateWithWatchdog result = %#v, want nil", result)
	}
	if err == nil || err.Error() != "The model context window is too small for this request." {
		t.Fatalf("GenerateWithWatchdog error = %v, want public context-budget failure", err)
	}
}

func TestSpawnContextSourceFragsDefersCustomSystemToFallback(t *testing.T) {
	t.Parallel()
	rc := RunConfig{System: "  custom spawn system\n", SessionType: sessionmode.Subagent}
	if got := SpawnContextSourceFrags(rc); got != nil {
		t.Fatalf("custom system source fragments = %#v, want legacy fallback", got)
	}
}
