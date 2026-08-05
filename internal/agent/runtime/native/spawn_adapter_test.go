package native

import (
	"context"
	"errors"
	"reflect"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	"github.com/memohai/memoh/internal/agent/sessionmode"
	tools "github.com/memohai/memoh/internal/agent/tool"
	"github.com/memohai/memoh/internal/apperror"
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

	plan := contextfrag.ContextBudgetPlan{
		Window:           4096,
		OutputReserve:    8192,
		ActualSystemCost: 123,
	}
	a := New(Deps{
		ContextViewApplier: spawnFailingBudgetApplier(plan, contextfrag.ErrBudgetUnsatisfied),
	})
	adapter := NewSpawnAdapter(a)
	provider := &preflightCountingProvider{}

	result, err := adapter.GenerateWithWatchdog(context.Background(), tools.SpawnRunConfig{
		Model: &sdk.Model{
			ID:       "spawn-preflight-error",
			Provider: provider,
			Type:     sdk.ModelTypeChat,
		},
		Query: "do the task",
	}, func() {})

	if result == nil || result.ContextLifecycle == nil {
		t.Fatalf("GenerateWithWatchdog result = %#v, want failure lifecycle audit", result)
	}
	if got := result.ContextLifecycle.BudgetPlan; got == nil || !reflect.DeepEqual(*got, plan) {
		t.Fatalf("failure lifecycle budget plan = %#v, want %#v", got, plan)
	}
	definition, ok := apperror.Lookup(apperror.CodeContextBudgetUnsatisfied)
	if !ok {
		t.Fatal("budget error missing from public catalog")
	}
	if err == nil || err.Error() != definition.Detail {
		t.Fatalf("GenerateWithWatchdog error = %v, want public context-budget failure", err)
	}
	if generateCalls, streamCalls := provider.calls(); generateCalls != 0 || streamCalls != 0 {
		t.Fatalf("provider calls = generate:%d stream:%d, want zero after preflight failure", generateCalls, streamCalls)
	}
}

func TestSpawnAdapterGenerateFailureCarriesContextLifecycle(t *testing.T) {
	t.Parallel()

	plan := contextfrag.ContextBudgetPlan{
		Window:           4096,
		OutputReserve:    8192,
		ActualSystemCost: 321,
	}
	a := New(Deps{
		ContextViewApplier: spawnFailingBudgetApplier(plan, contextfrag.ErrBudgetUnsatisfied),
	})
	adapter := NewSpawnAdapter(a)
	provider := &preflightCountingProvider{}

	result, err := adapter.Generate(context.Background(), tools.SpawnRunConfig{
		Model: &sdk.Model{
			ID:       "spawn-generate-preflight-error",
			Provider: provider,
			Type:     sdk.ModelTypeChat,
		},
		Query: "do the task",
	})

	if !errors.Is(err, contextfrag.ErrBudgetUnsatisfied) {
		t.Fatalf("Generate error = %v, want ErrBudgetUnsatisfied", err)
	}
	if result == nil || result.ContextLifecycle == nil {
		t.Fatalf("Generate result = %#v, want failure lifecycle audit", result)
	}
	if got := result.ContextLifecycle.BudgetPlan; got == nil || !reflect.DeepEqual(*got, plan) {
		t.Fatalf("failure lifecycle budget plan = %#v, want %#v", got, plan)
	}
	if generateCalls, streamCalls := provider.calls(); generateCalls != 0 || streamCalls != 0 {
		t.Fatalf("provider calls = generate:%d stream:%d, want zero after preflight failure", generateCalls, streamCalls)
	}
}

func spawnFailingBudgetApplier(plan contextfrag.ContextBudgetPlan, err error) ContextViewApplier {
	return func(_ context.Context, cfg RunConfig) (RunConfig, error) {
		manifest := contextfrag.Manifest{
			View:       contextfrag.ViewRunConfigPreProvider,
			BudgetPlan: &plan,
		}
		cfg.ContextManifest = manifest
		if cfg.ContextLifecycle != nil {
			cfg.ContextLifecycle.SetManifest(manifest)
		}
		return cfg, err
	}
}

func TestSpawnContextSourceFragsDefersCustomSystemToFallback(t *testing.T) {
	t.Parallel()
	rc := RunConfig{System: "  custom spawn system\n", SessionType: sessionmode.Subagent}
	if got := SpawnContextSourceFrags(rc); got != nil {
		t.Fatalf("custom system source fragments = %#v, want legacy fallback", got)
	}
}
