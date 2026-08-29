package native

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/apperror"
)

type staticToolProvider struct {
	tools []sdk.Tool
}

func (p staticToolProvider) Tools(context.Context, agenttools.SessionContext) ([]sdk.Tool, error) {
	return p.tools, nil
}

type atomicMockProvider struct {
	calls   atomic.Int32
	handler func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error)
	stream  func(ctx context.Context, params sdk.GenerateParams) (*sdk.StreamResult, error)
}

func (*atomicMockProvider) Name() string {
	return "mock"
}

func (*atomicMockProvider) ListModels(context.Context) ([]sdk.Model, error) {
	return nil, nil
}

func (*atomicMockProvider) Test(context.Context) *sdk.ProviderTestResult {
	return &sdk.ProviderTestResult{Status: sdk.ProviderStatusOK, Message: "ok"}
}

func (*atomicMockProvider) TestModel(context.Context, string) (*sdk.ModelTestResult, error) {
	return &sdk.ModelTestResult{Supported: true, Message: "supported"}, nil
}

func (m *atomicMockProvider) DoGenerate(_ context.Context, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
	call := int(m.calls.Add(1))
	return m.handler(call, params)
}

func (m *atomicMockProvider) DoStream(ctx context.Context, params sdk.GenerateParams) (*sdk.StreamResult, error) {
	if m.stream != nil {
		return m.stream(ctx, params)
	}

	result, err := m.DoGenerate(ctx, params)
	if err != nil {
		return nil, err
	}
	ch := make(chan sdk.StreamPart, 8)
	go func() {
		defer close(ch)
		ch <- &sdk.StartPart{}
		ch <- &sdk.StartStepPart{}
		if result.Text != "" {
			ch <- &sdk.TextStartPart{ID: "mock"}
			ch <- &sdk.TextDeltaPart{ID: "mock", Text: result.Text}
			ch <- &sdk.TextEndPart{ID: "mock"}
		}
		for _, tc := range result.ToolCalls {
			ch <- &sdk.StreamToolCallPart{
				ToolCallID: tc.ToolCallID,
				ToolName:   tc.ToolName,
				Input:      tc.Input,
			}
		}
		ch <- &sdk.FinishStepPart{
			FinishReason: result.FinishReason,
			Usage:        result.Usage,
			Response:     result.Response,
		}
		ch <- &sdk.FinishPart{
			FinishReason: result.FinishReason,
			TotalUsage:   result.Usage,
		}
	}()
	return &sdk.StreamResult{Stream: ch}, nil
}

func TestContextBudgetGuardProviderNeverDelegatesCanceledContext(t *testing.T) {
	t.Parallel()

	provider := &atomicMockProvider{
		handler: func(int, sdk.GenerateParams) (*sdk.GenerateResult, error) {
			return &sdk.GenerateResult{FinishReason: sdk.FinishReasonStop}, nil
		},
	}
	guarded := contextBudgetGuardProvider{Provider: provider}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := guarded.DoGenerate(ctx, sdk.GenerateParams{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("DoGenerate() error = %v, want context canceled", err)
	}
	if _, err := guarded.DoStream(ctx, sdk.GenerateParams{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("DoStream() error = %v, want context canceled", err)
	}
	if got := provider.calls.Load(); got != 0 {
		t.Fatalf("underlying provider calls = %d, want 0 for canceled context", got)
	}
}

func TestAgentGenerateStopsOnTerminalTextLoopAbort(t *testing.T) {
	t.Parallel()

	repeatedText := "abcdefghijklmnopqrstuvwxyz0123456789 repeated text chunk for loop detection"
	modelProvider := &atomicMockProvider{
		handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
			finishReason := sdk.FinishReasonToolCalls
			var toolCalls []sdk.ToolCall
			if call < 4 {
				toolCalls = []sdk.ToolCall{{
					ToolCallID: "call-terminal",
					ToolName:   "noop_tool",
					Input:      map[string]any{"step": call},
				}}
			} else {
				finishReason = sdk.FinishReasonStop
			}
			return &sdk.GenerateResult{
				Text:         repeatedText,
				FinishReason: finishReason,
				ToolCalls:    toolCalls,
			}, nil
		},
	}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{
			tools: []sdk.Tool{{
				Name:       "noop_tool",
				Parameters: &jsonschema.Schema{Type: "object"},
				Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
					return map[string]any{"ok": true}, nil
				},
			}},
		},
	})

	_, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("loop text terminal")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		LoopDetection:    LoopDetectionConfig{Enabled: true},
	})
	if !errors.Is(err, ErrTextLoopDetected) {
		t.Fatalf("expected ErrTextLoopDetected, got %v", err)
	}
	if modelProvider.calls.Load() != 4 {
		t.Fatalf("expected terminal text loop to abort on final step, got %d provider calls", modelProvider.calls.Load())
	}
}

func TestAgentGenerateRunsStepReselectorBeforeNextProviderCall(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	var secondCallMessages []sdk.Message
	modelProvider := &atomicMockProvider{
		handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
			switch call {
			case 1:
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "lookup",
						Input:      map[string]any{"q": "one"},
					}},
				}, nil
			case 2:
				secondCallMessages = append([]sdk.Message(nil), params.Messages...)
				return &sdk.GenerateResult{
					Text:         "ok",
					FinishReason: sdk.FinishReasonStop,
				}, nil
			default:
				return nil, errors.New("unexpected provider call")
			}
		},
	}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{
			tools: []sdk.Tool{{
				Name:       "lookup",
				Parameters: &jsonschema.Schema{Type: "object"},
				Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
					return map[string]any{"answer": strings.Repeat("tool-result ", 64)}, nil
				},
			}},
		},
	})

	var reselectorCalls atomic.Int32
	recentProtect := 4096
	_, err := a.Generate(context.Background(), RunConfig{
		Model:                      &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:                   []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall:           true,
		Identity:                   SessionContext{BotID: "bot-1"},
		ContextMutations:           ledger,
		ContextRecentProtectTokens: &recentProtect,
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			reselectorCalls.Add(1)
			if input.InitialMessageCount != 1 {
				t.Fatalf("InitialMessageCount = %d, want 1", input.InitialMessageCount)
			}
			if len(input.Messages) != 3 {
				t.Fatalf("selector input messages = %d, want 3", len(input.Messages))
			}
			// The resolved recent-protect window travels with the step
			// reselection input.
			if input.RecentProtectTokens == nil || *input.RecentProtectTokens != recentProtect {
				t.Fatalf("RecentProtectTokens = %v, want %d", input.RecentProtectTokens, recentProtect)
			}
			return ContextStepSelectionResult{
				Messages:    append([]sdk.Message(nil), input.Messages[:input.InitialMessageCount]...),
				Dropped:     len(input.Messages) - input.InitialMessageCount,
				DropReasons: map[string]int{"test": len(input.Messages) - input.InitialMessageCount},
			}
		},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if reselectorCalls.Load() != 1 {
		t.Fatalf("step reselector calls = %d, want 1", reselectorCalls.Load())
	}
	if len(secondCallMessages) != 1 {
		t.Fatalf("second provider call messages = %d, want 1", len(secondCallMessages))
	}
	if secondCallMessages[0].Role != sdk.MessageRoleUser {
		t.Fatalf("second provider call first role = %q, want user", secondCallMessages[0].Role)
	}
	records := ledger.Records()
	if len(records) != 1 || records[0].Kind != contextfrag.MutationLoopStepReselection {
		t.Fatalf("mutation records = %#v, want one loop_step_reselection", records)
	}
	if got := ledger.FinalInputHash(); got == "" {
		t.Fatal("final input hash was not updated after step reselection")
	}
}

func TestAgentGeneratePassesRemainingBudgetToStepReselector(t *testing.T) {
	t.Parallel()
	const budget = 1_000

	modelProvider := &atomicMockProvider{
		handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
			if call == 1 {
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-budget",
						ToolName:   "lookup",
						Input:      map[string]any{"q": "one"},
					}},
				}, nil
			}
			return &sdk.GenerateResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
		},
	}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{tools: []sdk.Tool{{
			Name:       "lookup",
			Parameters: &jsonschema.Schema{Type: "object"},
			Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
				return map[string]any{"answer": "ok"}, nil
			},
		}}},
	})

	var seenBudget int
	_, err := a.Generate(context.Background(), RunConfig{
		Model:                  &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:               []sdk.Message{sdk.UserMessage(strings.Repeat("prefix ", 80))},
		SupportsToolCall:       true,
		Identity:               SessionContext{BotID: "bot-1"},
		ContextMutations:       contextfrag.NewMutationLedger(),
		ContextBudgetMaxTokens: budget,
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			seenBudget = input.BudgetMaxTokens
			return ContextStepSelectionResult{}
		},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if seenBudget <= 0 || seenBudget >= budget {
		t.Fatalf("step budget = %d, want remaining budget below full run budget", seenBudget)
	}
}

func TestAgentGenerateActivePlanStepBudgetSubtractsFixedEnvelopeOnce(t *testing.T) {
	t.Parallel()

	lookupTool := sdk.Tool{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
			return map[string]any{"answer": "ok"}, nil
		},
	}
	toolCost := contextfrag.ToolDefAccountingFor("native", lookupTool).TokenEstimate
	plan := contextfrag.ContextBudgetPlan{
		Window:        2048,
		OutputReserve: toolCost + 200,
	}

	var firstParams sdk.GenerateParams
	modelProvider := &atomicMockProvider{
		handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
			if call == 1 {
				firstParams = cloneGenerateParams(params)
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-budget-plan",
						ToolName:   "lookup",
						Input:      map[string]any{"q": "one"},
					}},
				}, nil
			}
			return &sdk.GenerateResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
		},
	}

	a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
		return cfg, nil
	}})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{tools: []sdk.Tool{lookupTool}},
	})

	var seenBudget int
	var expectedBudget int
	_, err := a.Generate(context.Background(), RunConfig{
		Model:                  &sdk.Model{ID: "mock-model", Provider: modelProvider},
		System:                 "fixed system prefix",
		Messages:               []sdk.Message{sdk.UserMessage(strings.Repeat("prefix ", 20))},
		SupportsToolCall:       true,
		Identity:               SessionContext{BotID: "bot-1"},
		ContextMutations:       contextfrag.NewMutationLedger(),
		ContextBudgetMaxTokens: plan.Window,
		ContextManifest:        contextfrag.Manifest{BudgetPlan: &plan},
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			allowance := plan.Window - plan.OutputReserve
			expectedBudget = remainingStepBudget(allowance, &firstParams, input.InitialMessageCount)
			seenBudget = input.BudgetMaxTokens
			if input.ProviderSystem != firstParams.System || len(input.ProviderTools) != len(firstParams.Tools) {
				t.Fatalf("step provider envelope = system %q tools %d, want system %q tools %d", input.ProviderSystem, len(input.ProviderTools), firstParams.System, len(firstParams.Tools))
			}
			if input.ProviderInputAllowanceTokens != allowance {
				t.Fatalf("step provider allowance = %d, want %d", input.ProviderInputAllowanceTokens, allowance)
			}
			return ContextStepSelectionResult{}
		},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if seenBudget != expectedBudget {
		t.Fatalf("step budget = %d, want %d from window-output reserve minus the actual fixed prefix/tools once", seenBudget, expectedBudget)
	}
	legacyDoubleCounted := remainingStepBudget(plan.Window-toolCost, &firstParams, len(firstParams.Messages))
	if expectedBudget == legacyDoubleCounted {
		t.Fatalf("test setup does not distinguish active plan allowance %d from legacy double-counted allowance %d", expectedBudget, legacyDoubleCounted)
	}
}

func TestAgentGenerateFailsClosedOnProtectedStepOverflow(t *testing.T) {
	t.Parallel()

	modelProvider := &atomicMockProvider{
		handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
			if call != 1 {
				return nil, fmt.Errorf("unexpected provider call %d after protected overflow", call)
			}
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls: []sdk.ToolCall{{
					ToolCallID: "call-step-overflow",
					ToolName:   "lookup",
					Input:      map[string]any{"q": "one"},
				}},
			}, nil
		},
	}
	ledger := contextfrag.NewMutationLedger()
	a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
		return cfg, nil
	}})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{tools: []sdk.Tool{{
			Name:       "lookup",
			Parameters: &jsonschema.Schema{Type: "object"},
			Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
				return map[string]any{"answer": "ok"}, nil
			},
		}}},
	})

	_, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
		ContextStepReselector: func(context.Context, ContextStepSelectionInput) ContextStepSelectionResult {
			return ContextStepSelectionResult{FatalError: contextfrag.ErrProtectedContextOverflow}
		},
	})
	if !errors.Is(err, contextfrag.ErrProtectedContextOverflow) {
		t.Fatalf("Generate() error = %v, want %v", err, contextfrag.ErrProtectedContextOverflow)
	}
	if got := modelProvider.calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
	steps := ledger.StepSnapshots()
	if len(steps) != 2 || steps[0].StepIndex != 0 || steps[1].StepIndex != 1 {
		t.Fatalf("step snapshots = %#v, want exactly one entry for initial and failed steps", steps)
	}
	if steps[1].PostPrepareInputHash != "" {
		t.Fatalf("failed step snapshot has provider input hash %q, want none", steps[1].PostPrepareInputHash)
	}
	records := ledger.Records()
	if len(records) != 1 ||
		records[0].Kind != contextfrag.MutationContextBudgetFailure ||
		records[0].Detail != "protected_context_overflow" {
		t.Fatalf("budget failure mutations = %#v, want one protected-overflow record", records)
	}
}

func TestAgentStreamFailsClosedOnProtectedStepOverflow(t *testing.T) {
	t.Parallel()

	modelProvider := &atomicMockProvider{
		handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
			if call != 1 {
				return nil, fmt.Errorf("unexpected provider call %d after protected overflow", call)
			}
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls: []sdk.ToolCall{{
					ToolCallID: "call-stream-step-overflow",
					ToolName:   "lookup",
					Input:      map[string]any{"q": "one"},
				}},
			}, nil
		},
	}
	a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
		return cfg, nil
	}})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{tools: []sdk.Tool{{
			Name:       "lookup",
			Parameters: &jsonschema.Schema{Type: "object"},
			Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
				return map[string]any{"answer": "ok"}, nil
			},
		}}},
	})

	var errorEvents []StreamEvent
	for event := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		ContextStepReselector: func(context.Context, ContextStepSelectionInput) ContextStepSelectionResult {
			return ContextStepSelectionResult{FatalError: contextfrag.ErrProtectedContextOverflow}
		},
	}) {
		if event.Type == EventError {
			errorEvents = append(errorEvents, event)
		}
	}

	if got := modelProvider.calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
	if len(errorEvents) != 1 {
		t.Fatalf("error events = %#v, want exactly one", errorEvents)
	}
	if errorEvents[0].Code != string(apperror.CodeContextProtectedOverflow) {
		t.Fatalf("error code = %q, want %q", errorEvents[0].Code, apperror.CodeContextProtectedOverflow)
	}
}
