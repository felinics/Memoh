package contextview

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/models"
)

// photoPrefix is a vision turn whose user message carries a JPEG of the given
// base64 length as an inline data URL, the shape every channel adapter
// produces for attachments.
func photoPrefix(base64Length int) []sdk.Message {
	return []sdk.Message{{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{
		sdk.TextPart{Text: "what's in this photo, and what's the weather there?"},
		sdk.ImagePart{Image: "data:image/jpeg;base64," + strings.Repeat("A", base64Length), MediaType: "image/jpeg"},
	}}}
}

func TestProviderStepReselectionKeepsPhotoInFrozenPrefixWithinEnvelope(t *testing.T) {
	t.Parallel()

	prefix := photoPrefix(400_000)
	messages := append([]sdk.Message(nil), prefix...)
	messages = append(messages,
		assistantToolCallMessage("call-weather", "lookup", ""),
		toolResultMessage("call-weather", "lookup", "sunny, 25C"),
	)
	system := strings.Repeat("s", 8_000)
	tools := []sdk.Tool{{Name: "lookup", Description: "Look something up.", Parameters: map[string]any{"type": "object"}}}

	plan, err := ComputeContextBudgetPlan(128_000, models.DefaultOutputReserveTokens, 0, 0)
	if err != nil {
		t.Fatalf("ComputeContextBudgetPlan: %v", err)
	}
	allowance := plan.Window - plan.OutputReserve
	prefixCost := contextfrag.ProviderEnvelopeTokens(system, prefix, tools)
	if prefixCost > allowance/4 {
		t.Fatalf("photo prefix priced at %d tokens, want a flat media estimate well inside allowance %d", prefixCost, allowance)
	}

	protect := DefaultRecentProtectTokens
	result := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:                        contextfrag.Scope{BotID: "bot-1"},
		InitialMessageCount:          len(prefix),
		Messages:                     messages,
		BudgetMaxTokens:              allowance - prefixCost,
		ProviderSystem:               system,
		ProviderTools:                tools,
		ProviderInputAllowanceTokens: allowance,
		RecentProtectTokens:          &protect,
		KeepRecentToolResults:        2,
		MinMessages:                  4,
	})
	if result.FatalError != nil {
		t.Fatalf("step reselection failed for a photo prefix that fits the window: %v", result.FatalError)
	}
	if result.Messages != nil || result.Dropped != 0 || result.Truncated != 0 {
		t.Fatalf("step reselection changed a fitting tool loop: %+v", result)
	}
}

type envelopeProbeToolProvider struct{ tools []sdk.Tool }

func (p envelopeProbeToolProvider) Tools(context.Context, agenttools.SessionContext) ([]sdk.Tool, error) {
	return p.tools, nil
}

type envelopeProbeProvider struct {
	calls   atomic.Int32
	handler func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error)
}

func (*envelopeProbeProvider) Name() string { return "mock" }

func (*envelopeProbeProvider) ListModels(context.Context) ([]sdk.Model, error) { return nil, nil }

func (*envelopeProbeProvider) Test(context.Context) *sdk.ProviderTestResult {
	return &sdk.ProviderTestResult{Status: sdk.ProviderStatusOK}
}

func (*envelopeProbeProvider) TestModel(context.Context, string) (*sdk.ModelTestResult, error) {
	return &sdk.ModelTestResult{Supported: true}, nil
}

func (p *envelopeProbeProvider) DoGenerate(_ context.Context, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
	return p.handler(int(p.calls.Add(1)), params)
}

func (*envelopeProbeProvider) DoStream(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
	return nil, nil
}

func TestAgentToolLoopSurvivesPhotoInFrozenPrefix(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		window int
	}{
		{name: "128k window", window: 128_000},
		{name: "200k window", window: 200_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := &envelopeProbeProvider{handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
				if call == 1 {
					return &sdk.GenerateResult{FinishReason: sdk.FinishReasonToolCalls, ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-weather", ToolName: "lookup", Input: map[string]any{"q": "weather"},
					}}}, nil
				}
				return &sdk.GenerateResult{Text: "sunny", FinishReason: sdk.FinishReasonStop}, nil
			}}
			agent := agentpkg.New(agentpkg.Deps{ContextViewApplier: ProviderRunConfigApplier(nil)})
			agent.SetToolProviders([]agenttools.ToolProvider{envelopeProbeToolProvider{tools: []sdk.Tool{{
				Name:       "lookup",
				Parameters: &jsonschema.Schema{Type: "object"},
				Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
					return map[string]any{"weather": "sunny"}, nil
				},
			}}}})
			currentIndex := 0
			ledger := contextfrag.NewMutationLedger()
			lifecycle := contextfrag.NewLifecycleHolder()
			_, genErr := agent.Generate(context.Background(), agentpkg.RunConfig{
				Model:                          &sdk.Model{ID: "vision-model", Provider: provider, Type: sdk.ModelTypeChat},
				System:                         "you are helpful",
				Messages:                       photoPrefix(1_000_000),
				ContextCurrentUserMessageIndex: &currentIndex,
				ContextBudgetMaxTokens:         tc.window,
				SupportsToolCall:               true,
				SupportsImageInput:             true,
				Identity:                       agentpkg.SessionContext{BotID: "bot-1"},
				ContextMutations:               ledger,
				ContextLifecycle:               lifecycle,
			})
			if genErr != nil {
				t.Fatalf("Generate() error = %v, mutations = %+v", genErr, ledger.Records())
			}
			if calls := provider.calls.Load(); calls != 2 {
				t.Fatalf("provider calls = %d, want the tool step to dispatch after the photo turn", calls)
			}
			for _, record := range ledger.Records() {
				if record.Kind == contextfrag.MutationContextBudgetFailure || record.Kind == contextfrag.MutationContextViewFallback {
					t.Fatalf("photo prefix left the fragment-first path: %+v", record)
				}
			}
			snapshot, ok := lifecycle.Snapshot()
			if !ok || snapshot.BudgetPlan == nil || snapshot.BudgetPlan.Window != tc.window {
				t.Fatalf("lifecycle snapshot = %+v, want the active budget plan for window %d", snapshot.BudgetPlan, tc.window)
			}
		})
	}
}
