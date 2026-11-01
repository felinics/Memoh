package native

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/models"
)

func oversizedPrefixRunConfig(provider sdk.Provider, plan *contextfrag.ContextBudgetPlan) RunConfig {
	return RunConfig{
		Model:                  &sdk.Model{ID: "mock-model", Provider: provider},
		System:                 "system",
		Messages:               []sdk.Message{sdk.UserMessage(strings.Repeat("oversized ", 2_000))},
		Identity:               SessionContext{BotID: "bot-1"},
		ContextMutations:       contextfrag.NewMutationLedger(),
		ContextBudgetMaxTokens: plan.Window,
		ContextManifest:        contextfrag.Manifest{BudgetPlan: plan},
	}
}

func TestAgentGenerateChecksInitialEnvelope(t *testing.T) {
	t.Parallel()

	modelProvider := &atomicMockProvider{handler: func(call int, _ sdk.Request) (sdk.ModelResult, error) {
		return sdk.ModelResult{}, fmt.Errorf("provider must not be called for an oversized prefix (call %d)", call)
	}}
	a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
		return cfg, nil
	}})
	plan := contextfrag.ContextBudgetPlan{Window: 2_000, OutputReserve: 500}
	cfg := oversizedPrefixRunConfig(modelProvider, &plan)

	_, err := a.Generate(context.Background(), cfg)
	if !errors.Is(err, contextfrag.ErrBudgetUnsatisfied) {
		t.Fatalf("Generate() error = %v, want %v", err, contextfrag.ErrBudgetUnsatisfied)
	}
	if got := modelProvider.calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
	records := cfg.ContextMutations.Records()
	if len(records) != 1 || records[0].Kind != contextfrag.MutationContextBudgetFailure {
		t.Fatalf("mutations = %#v, want one context budget failure", records)
	}
	assertRejectedInitialStep(t, cfg.ContextMutations)
}

func assertRejectedInitialStep(t *testing.T, ledger *contextfrag.MutationLedger) {
	t.Helper()
	steps := ledger.StepSnapshots()
	if len(steps) != 1 {
		t.Fatalf("step snapshots = %#v, want exactly the rejected initial step", steps)
	}
	if steps[0].StepIndex != 0 || steps[0].PostPrepareInputHash != "" {
		t.Fatalf("initial step snapshot = %#v, want step 0 rejected before dispatch", steps[0])
	}
}

func TestProviderInputAllowanceWithoutPlanReservesResolvedOutput(t *testing.T) {
	t.Parallel()

	cfg := RunConfig{
		Model:                  &sdk.Model{ID: "claude", Provider: anthropicNameMockProvider{&atomicMockProvider{}}},
		ReasoningConfig:        &models.ReasoningConfig{Active: true, Adaptive: true, Effort: models.ReasoningEffortHigh},
		ContextBudgetMaxTokens: 200_000,
		ContextToolDefs:        []contextfrag.ToolDefAccounting{{Name: "lookup", TokenEstimate: 900}},
	}
	if got := providerInputAllowance(cfg); got != 200_000-32_000 {
		t.Fatalf("allowance without a plan = %d, want window minus the resolved output reserve", got)
	}
	if got := providerInputAllowance(RunConfig{}); got != 0 {
		t.Fatalf("allowance without a window = %d, want budgeting disabled", got)
	}
}
