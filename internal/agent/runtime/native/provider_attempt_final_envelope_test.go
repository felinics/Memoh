package native

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
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

func TestAgentGenerateChecksInitialEnvelopeWithoutReselector(t *testing.T) {
	t.Parallel()

	modelProvider := &atomicMockProvider{handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
		return nil, fmt.Errorf("provider must not be called for an oversized prefix (call %d)", call)
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
	if steps[0].StepIndex != 0 || steps[0].ReselectionApplied || steps[0].ReselectionOutcome != contextfrag.ReselectionOutcomeFailed {
		t.Fatalf("initial step snapshot = %#v, want step 0 failed without reselection", steps[0])
	}
}

func TestAgentGenerateChecksFullPrefixInitialEnvelopeWithReselector(t *testing.T) {
	t.Parallel()

	modelProvider := &atomicMockProvider{handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
		return nil, fmt.Errorf("provider must not be called for an oversized prefix (call %d)", call)
	}}
	a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
		return cfg, nil
	}})
	plan := contextfrag.ContextBudgetPlan{Window: 2_000, OutputReserve: 500}
	cfg := oversizedPrefixRunConfig(modelProvider, &plan)
	reselectorCalls := 0
	cfg.ContextStepReselector = func(context.Context, ContextStepSelectionInput) ContextStepSelectionResult {
		reselectorCalls++
		return ContextStepSelectionResult{}
	}

	_, err := a.Generate(context.Background(), cfg)
	if !errors.Is(err, contextfrag.ErrBudgetUnsatisfied) {
		t.Fatalf("Generate() error = %v, want %v", err, contextfrag.ErrBudgetUnsatisfied)
	}
	if got := modelProvider.calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
	if reselectorCalls != 0 {
		t.Fatalf("reselector calls = %d, want none for a prefix-only initial dispatch", reselectorCalls)
	}
	assertRejectedInitialStep(t, cfg.ContextMutations)
}

func TestAgentGenerateShadowModeStillFailsClosedOnEnvelopeOverflow(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		reselector  ContextStepReselector
		want        string
		wantDropped bool
	}{
		{
			name: "would apply",
			reselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
				return ContextStepSelectionResult{
					Messages: append([]sdk.Message(nil), input.Messages[:input.InitialMessageCount]...),
					Dropped:  len(input.Messages) - input.InitialMessageCount,
				}
			},
			want:        contextfrag.ReselectionOutcomeWouldApply,
			wantDropped: true,
		},
		{
			name: "would fail",
			reselector: func(context.Context, ContextStepSelectionInput) ContextStepSelectionResult {
				return ContextStepSelectionResult{FatalError: contextfrag.ErrProtectedContextOverflow}
			},
			want: contextfrag.ReselectionOutcomeWouldFail,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lookupTool := sdk.Tool{
				Name:       "lookup",
				Parameters: &jsonschema.Schema{Type: "object"},
				Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
					return strings.Repeat("large-result ", 1_000), nil
				},
			}
			modelProvider := &atomicMockProvider{handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
				if call != 1 {
					return nil, fmt.Errorf("unexpected provider call %d after envelope overflow", call)
				}
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-shadow", ToolName: "lookup", Input: map[string]any{"q": "one"},
					}},
				}, nil
			}}
			a := New(Deps{
				LoopReselectMode: LoopReselectShadow,
				ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
					return cfg, nil
				},
			})
			a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{lookupTool}}})
			plan := contextfrag.ContextBudgetPlan{Window: 2_000, OutputReserve: 100}
			ledger := contextfrag.NewMutationLedger()

			_, err := a.Generate(context.Background(), RunConfig{
				Model:                  &sdk.Model{ID: "mock-model", Provider: modelProvider},
				System:                 "system",
				Messages:               []sdk.Message{sdk.UserMessage("task")},
				SupportsToolCall:       true,
				Identity:               SessionContext{BotID: "bot-1"},
				ContextMutations:       ledger,
				ContextBudgetMaxTokens: plan.Window,
				ContextManifest:        contextfrag.Manifest{BudgetPlan: &plan},
				ContextStepReselector:  tc.reselector,
			})
			if !errors.Is(err, contextfrag.ErrBudgetUnsatisfied) {
				t.Fatalf("Generate() error = %v, want %v", err, contextfrag.ErrBudgetUnsatisfied)
			}
			if got := modelProvider.calls.Load(); got != 1 {
				t.Fatalf("provider calls = %d, want the initial call only", got)
			}
			if mode := ledger.LoopSelectionMode(); mode != contextfrag.LoopSelectionSuffixOnlyShadow {
				t.Fatalf("loop selection mode = %q, want shadow", mode)
			}
			steps := ledger.StepSnapshots()
			if len(steps) != 2 {
				t.Fatalf("step snapshots = %#v, want the dispatched step and the rejected shadow step", steps)
			}
			if rejected := steps[1]; rejected.ReselectionOutcome != tc.want || rejected.ReselectionApplied || (rejected.Dropped > 0) != tc.wantDropped {
				t.Fatalf("shadow step snapshot = %#v, want the observed %s verdict kept on the rejected step", rejected, tc.want)
			}
		})
	}
}

func TestStepReselectionAllowanceWithoutPlanReservesResolvedOutput(t *testing.T) {
	t.Parallel()

	cfg := RunConfig{
		Model:                  &sdk.Model{ID: "claude", Provider: anthropicNameMockProvider{&atomicMockProvider{}}},
		ReasoningConfig:        &models.ReasoningConfig{Active: true, Adaptive: true, Effort: models.ReasoningEffortHigh},
		ContextBudgetMaxTokens: 200_000,
		ContextToolDefs:        []contextfrag.ToolDefAccounting{{Name: "lookup", TokenEstimate: 900}},
	}
	if got := stepReselectionAllowance(cfg); got != 200_000-32_000 {
		t.Fatalf("allowance without a plan = %d, want window minus the resolved output reserve", got)
	}
	if got := stepReselectionAllowance(RunConfig{}); got != 0 {
		t.Fatalf("allowance without a window = %d, want budgeting disabled", got)
	}
}
