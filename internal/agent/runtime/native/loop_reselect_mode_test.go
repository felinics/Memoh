package native

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
)

func TestNewAgentDefaultsToActiveLoopReselectMode(t *testing.T) {
	t.Parallel()

	if got := New(Deps{}).LoopReselectMode(); got != LoopReselectActive {
		t.Fatalf("New(Deps{}).LoopReselectMode() = %q, want %q", got, LoopReselectActive)
	}
	if got := (*Agent)(nil).LoopReselectMode(); got != LoopReselectActive {
		t.Fatalf("nil Agent LoopReselectMode() = %q, want %q", got, LoopReselectActive)
	}
	if got := New(Deps{LoopReselectMode: "garbage"}).LoopReselectMode(); got != LoopReselectActive {
		t.Fatalf("unrecognized LoopReselectMode = %q, want %q", got, LoopReselectActive)
	}
}

func TestBuildGenerateOptionsOffModeSetsLegacyPruneLoopSelectionMode(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	model := &sdk.Model{ID: "mock-model", Provider: &usageRecordingProvider{}, Type: sdk.ModelTypeChat}
	a := New(Deps{LoopReselectMode: LoopReselectOff})
	cfg := RunConfig{
		Model:            model,
		System:           "sys",
		Messages:         []sdk.Message{sdk.UserMessage("hi")},
		ContextMutations: ledger,
		ContextStepReselector: func(context.Context, ContextStepSelectionInput) ContextStepSelectionResult {
			return ContextStepSelectionResult{}
		},
	}

	a.buildGenerateOptions(context.Background(), cfg, nil, nil, nil)

	if got := ledger.LoopSelectionMode(); got != contextfrag.LoopSelectionLegacyPrune {
		t.Fatalf("loop selection mode = %q, want %q", got, contextfrag.LoopSelectionLegacyPrune)
	}
}

func TestBuildGenerateOptionsShadowModeSetsSuffixOnlyShadowLoopSelectionMode(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	model := &sdk.Model{ID: "mock-model", Provider: &usageRecordingProvider{}, Type: sdk.ModelTypeChat}
	a := New(Deps{LoopReselectMode: LoopReselectShadow})
	cfg := RunConfig{
		Model:            model,
		System:           "sys",
		Messages:         []sdk.Message{sdk.UserMessage("hi")},
		ContextMutations: ledger,
		ContextStepReselector: func(context.Context, ContextStepSelectionInput) ContextStepSelectionResult {
			return ContextStepSelectionResult{}
		},
	}

	a.buildGenerateOptions(context.Background(), cfg, nil, nil, nil)

	if got := ledger.LoopSelectionMode(); got != contextfrag.LoopSelectionSuffixOnlyShadow {
		t.Fatalf("loop selection mode = %q, want %q", got, contextfrag.LoopSelectionSuffixOnlyShadow)
	}
}

func mockToolLoopProvider(maxToolCall int, idPrefix string) *atomicMockProvider {
	return &atomicMockProvider{
		handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
			if call <= maxToolCall {
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: fmt.Sprintf("%s-%d", idPrefix, call),
						ToolName:   "lookup",
						Input:      map[string]any{"step": call},
					}},
				}, nil
			}
			return &sdk.GenerateResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
		},
	}
}

func mockToolLoopTools() []agenttools.ToolProvider {
	return []agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
			return strings.Repeat("large tool result ", 80), nil
		},
	}}}}
}

// TestAgentGenerateOffModeNeverInvokesReselectorAndMatchesReselectorNilRun
// drives off mode with a non-nil counting reselector and asserts it is never
// called, the ledger records legacy_prune, and the resulting provider
// payload sequence is identical to a control run that never had a
// reselector configured at all.
func TestAgentGenerateOffModeNeverInvokesReselectorAndMatchesReselectorNilRun(t *testing.T) {
	t.Parallel()

	captureParams := func(a *Agent, cfg RunConfig) []sdk.GenerateParams {
		var params []sdk.GenerateParams
		provider := cfg.Model.Provider.(*atomicMockProvider)
		baseHandler := provider.handler
		provider.handler = func(call int, p sdk.GenerateParams) (*sdk.GenerateResult, error) {
			params = append(params, sdk.GenerateParams{
				System:   p.System,
				Messages: append([]sdk.Message(nil), p.Messages...),
				Tools:    append([]sdk.Tool(nil), p.Tools...),
			})
			return baseHandler(call, p)
		}
		_, err := a.Generate(context.Background(), cfg)
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		return params
	}

	controlAgent := New(Deps{})
	controlAgent.SetToolProviders(mockToolLoopTools())
	controlParams := captureParams(controlAgent, RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: mockToolLoopProvider(5, "call")},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
	})

	var reselectorCalls atomic.Int32
	offAgent := New(Deps{LoopReselectMode: LoopReselectOff})
	offAgent.SetToolProviders(mockToolLoopTools())
	ledger := contextfrag.NewMutationLedger()
	offParams := captureParams(offAgent, RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: mockToolLoopProvider(5, "call")},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
		ContextStepReselector: func(context.Context, ContextStepSelectionInput) ContextStepSelectionResult {
			reselectorCalls.Add(1)
			return ContextStepSelectionResult{}
		},
	})

	if reselectorCalls.Load() != 0 {
		t.Fatalf("reselector calls = %d, want 0 in off mode", reselectorCalls.Load())
	}
	if got := ledger.LoopSelectionMode(); got != contextfrag.LoopSelectionLegacyPrune {
		t.Fatalf("loop selection mode = %q, want %q", got, contextfrag.LoopSelectionLegacyPrune)
	}
	if len(offParams) != len(controlParams) {
		t.Fatalf("off-mode provider calls = %d, want %d (matching reselector-nil control run)", len(offParams), len(controlParams))
	}
	for i := range offParams {
		if !reflect.DeepEqual(offParams[i].Messages, controlParams[i].Messages) {
			t.Fatalf("off-mode call %d messages = %#v, want control messages %#v", i, offParams[i].Messages, controlParams[i].Messages)
		}
	}
}

// TestAgentGenerateShadowModeInvokesReselectorButNeverAppliesSelection drives
// a scenario where the reselector wants to drop suffix messages. Shadow mode
// must still invoke it (for observability), but the outgoing payload must
// match a pure legacy-prune (reselector-nil) run, and the StepSnapshot must
// carry the reselector's would-be verdict with ReselectionApplied=false.
func TestAgentGenerateShadowModeInvokesReselectorButNeverAppliesSelection(t *testing.T) {
	t.Parallel()

	captureParams := func(a *Agent, cfg RunConfig) []sdk.GenerateParams {
		var params []sdk.GenerateParams
		provider := cfg.Model.Provider.(*atomicMockProvider)
		baseHandler := provider.handler
		provider.handler = func(call int, p sdk.GenerateParams) (*sdk.GenerateResult, error) {
			params = append(params, sdk.GenerateParams{
				System:   p.System,
				Messages: append([]sdk.Message(nil), p.Messages...),
				Tools:    append([]sdk.Tool(nil), p.Tools...),
			})
			return baseHandler(call, p)
		}
		_, err := a.Generate(context.Background(), cfg)
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		return params
	}

	controlAgent := New(Deps{})
	controlAgent.SetToolProviders(mockToolLoopTools())
	controlParams := captureParams(controlAgent, RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: mockToolLoopProvider(5, "call")},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
	})

	var reselectorCalls atomic.Int32
	shadowAgent := New(Deps{LoopReselectMode: LoopReselectShadow})
	shadowAgent.SetToolProviders(mockToolLoopTools())
	ledger := contextfrag.NewMutationLedger()
	shadowParams := captureParams(shadowAgent, RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: mockToolLoopProvider(5, "call")},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			reselectorCalls.Add(1)
			dropped := len(input.Messages) - input.InitialMessageCount
			return ContextStepSelectionResult{
				Messages:    append([]sdk.Message(nil), input.Messages[:input.InitialMessageCount]...),
				Dropped:     dropped,
				DropReasons: map[string]int{"shadow-test": dropped},
			}
		},
	})

	if reselectorCalls.Load() == 0 {
		t.Fatal("shadow mode must invoke the step reselector")
	}
	if got := ledger.LoopSelectionMode(); got != contextfrag.LoopSelectionSuffixOnlyShadow {
		t.Fatalf("loop selection mode = %q, want %q", got, contextfrag.LoopSelectionSuffixOnlyShadow)
	}
	if len(shadowParams) != len(controlParams) {
		t.Fatalf("shadow-mode provider calls = %d, want %d (matching reselector-nil control run)", len(shadowParams), len(controlParams))
	}
	for i := range shadowParams {
		if !reflect.DeepEqual(shadowParams[i].Messages, controlParams[i].Messages) {
			t.Fatalf("shadow-mode call %d messages = %#v, want control messages %#v (selection must never be applied)", i, shadowParams[i].Messages, controlParams[i].Messages)
		}
	}

	var sawShadowVerdict bool
	for _, step := range ledger.StepSnapshots() {
		if step.StepIndex == 0 {
			continue
		}
		if step.ReselectionApplied {
			t.Fatalf("steps[%d].ReselectionApplied = true, want false in shadow mode: %#v", step.StepIndex, step)
		}
		if step.Dropped > 0 {
			sawShadowVerdict = true
			if len(step.DropReasons) == 0 {
				t.Fatalf("steps[%d] want would-be drop reasons recorded, got none: %#v", step.StepIndex, step)
			}
		}
	}
	if !sawShadowVerdict {
		t.Fatalf("step snapshots = %#v, want at least one step with the shadow reselector's would-be Dropped > 0", ledger.StepSnapshots())
	}

	for _, r := range ledger.Records() {
		if r.Kind == contextfrag.MutationLoopStepReselection {
			t.Fatalf("shadow mode must not apply reselection; unexpected loop_step_reselection record: %#v", r)
		}
	}
}

func TestAgentGenerateShadowModeDoesNotApplyFatalReselection(t *testing.T) {
	t.Parallel()

	var reselectorCalls atomic.Int32
	ledger := contextfrag.NewMutationLedger()
	provider := mockToolLoopProvider(1, "call-shadow-fatal")
	a := New(Deps{LoopReselectMode: LoopReselectShadow})
	a.SetToolProviders(mockToolLoopTools())

	_, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
		ContextStepReselector: func(context.Context, ContextStepSelectionInput) ContextStepSelectionResult {
			reselectorCalls.Add(1)
			return ContextStepSelectionResult{FatalError: contextfrag.ErrProtectedContextOverflow}
		},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v, want shadow mode to preserve the legacy run", err)
	}
	if reselectorCalls.Load() != 1 {
		t.Fatalf("reselector calls = %d, want 1", reselectorCalls.Load())
	}
	if got := provider.calls.Load(); got != 2 {
		t.Fatalf("provider calls = %d, want both legacy model steps", got)
	}
	for _, record := range ledger.Records() {
		if record.Kind == contextfrag.MutationContextBudgetFailure {
			t.Fatalf("shadow mode applied fatal reselection: %#v", ledger.Records())
		}
	}
}
