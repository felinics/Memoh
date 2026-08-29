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
)

func TestAgentGenerateActivePreflightBlocksSerializedOverflowFromNoopSelector(t *testing.T) {
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
			return nil, fmt.Errorf("unexpected provider call %d after serialized overflow", call)
		}
		return &sdk.GenerateResult{
			FinishReason: sdk.FinishReasonToolCalls,
			ToolCalls: []sdk.ToolCall{{
				ToolCallID: "call-envelope", ToolName: "lookup", Input: map[string]any{"q": "one"},
			}},
		}, nil
	}}
	a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
		return cfg, nil
	}})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{lookupTool}}})
	plan := contextfrag.ContextBudgetPlan{Window: 2_000, OutputReserve: 100}

	_, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		System:           "system",
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		ContextManifest:  contextfrag.Manifest{BudgetPlan: &plan},
		ContextStepReselector: func(context.Context, ContextStepSelectionInput) ContextStepSelectionResult {
			return ContextStepSelectionResult{}
		},
	})
	if !errors.Is(err, contextfrag.ErrBudgetUnsatisfied) {
		t.Fatalf("Generate() error = %v, want %v", err, contextfrag.ErrBudgetUnsatisfied)
	}
	if got := modelProvider.calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want the initial call only", got)
	}
}
