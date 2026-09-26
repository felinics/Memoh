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
	"github.com/felinics/memoh/internal/agent/step"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/apperror"
)

type staticToolProvider struct {
	tools []toolexec.Tool
}

func (p staticToolProvider) Tools(context.Context, agenttools.SessionContext) ([]toolexec.Tool, error) {
	return p.tools, nil
}

type atomicMockProvider struct {
	calls   atomic.Int32
	handler func(call int, params sdk.Request) (sdk.ModelResult, error)
	stream  func(ctx context.Context, params sdk.Request) (<-chan sdk.StreamPart, error)
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

func (m *atomicMockProvider) DoGenerate(_ context.Context, params sdk.Request) (sdk.ModelResult, error) {
	call := int(m.calls.Add(1))
	return m.handler(call, params)
}

func (m *atomicMockProvider) DoStream(ctx context.Context, params sdk.Request) (<-chan sdk.StreamPart, error) {
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
				Input:      toolexec.ArgumentsFromValue(tc.Input),
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
	return ch, nil
}

func TestAgentGenerateNeverDispatchesOnCanceledContext(t *testing.T) {
	t.Parallel()

	provider := &atomicMockProvider{
		handler: func(int, sdk.Request) (sdk.ModelResult, error) {
			return sdk.ModelResult{FinishReason: sdk.FinishReasonStop}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	a := New(Deps{})
	if _, err := a.Generate(ctx, RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate() error = %v, want context canceled", err)
	}
	if got := provider.calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0 for canceled context", got)
	}
}

func TestAgentGenerateContinuesAfterFinalSteer(t *testing.T) {
	t.Parallel()

	provider := &atomicMockProvider{
		handler: func(call int, params sdk.Request) (sdk.ModelResult, error) {
			if call == 2 {
				if !providerAttemptContainsText(params.Messages, "change direction") {
					t.Fatalf("second provider call lost steer: %#v", params.Messages)
				}
				return sdk.ModelResult{Text: "adjusted", FinishReason: sdk.FinishReasonStop}, nil
			}
			return sdk.ModelResult{Text: "answer", FinishReason: sdk.FinishReasonStop}, nil
		},
	}
	a := New(Deps{})
	var commits int
	result, err := a.Generate(context.Background(), RunConfig{
		Model:    &sdk.Model{ID: "mock-model", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage("hello")},
		Identity: SessionContext{BotID: "bot-1"},
		OnStepCommitted: func(_ context.Context, _ int, _ *step.Record) (StepDirective, error) {
			commits++
			if commits == 1 {
				return StepDirective{NextInputs: []DirectiveInput{{Text: "change direction"}}}, nil
			}
			return StepDirective{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if provider.calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls.Load())
	}
	// The answer before the steer stays in the result, joined the way the
	// segment continuation joined it before the loop moved in-process.
	if result == nil || result.Text != "answer\nadjusted" {
		t.Fatalf("result text = %q, want the pre-steer answer kept", result.Text)
	}
}

func TestAgentStreamNeverDispatchesOnCanceledContext(t *testing.T) {
	t.Parallel()

	provider := &atomicMockProvider{
		handler: func(int, sdk.Request) (sdk.ModelResult, error) {
			return sdk.ModelResult{FinishReason: sdk.FinishReasonStop}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	a := New(Deps{})
	var terminal StreamEvent
	for event := range a.Stream(ctx, RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
	}) {
		if event.IsTerminal() {
			terminal = event
		}
	}
	if terminal.Type != EventAgentAbort {
		t.Fatalf("terminal event = %q, want %q", terminal.Type, EventAgentAbort)
	}
	if got := provider.calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0 for canceled context", got)
	}
}

func TestAgentGenerateStopsOnTerminalTextLoopAbort(t *testing.T) {
	t.Parallel()

	repeatedText := "abcdefghijklmnopqrstuvwxyz0123456789 repeated text chunk for loop detection"
	modelProvider := &atomicMockProvider{
		handler: func(call int, _ sdk.Request) (sdk.ModelResult, error) {
			finishReason := sdk.FinishReasonToolCalls
			var toolCalls []sdk.ToolCall
			if call < 4 {
				toolCalls = []sdk.ToolCall{{
					ToolCallID: "call-terminal",
					ToolName:   "noop_tool",
					Input:      toolexec.ArgumentsFromValue(map[string]any{"step": call}),
				}}
			} else {
				finishReason = sdk.FinishReasonStop
			}
			return sdk.ModelResult{
				Text:         repeatedText,
				FinishReason: finishReason,
				ToolCalls:    toolCalls,
			}, nil
		},
	}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{
			tools: []toolexec.Tool{{
				Name:       "noop_tool",
				Parameters: &jsonschema.Schema{Type: "object"},
				Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
					return toolexec.OutputFromValue(map[string]any{"ok": true}), nil
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
		handler: func(call int, params sdk.Request) (sdk.ModelResult, error) {
			switch call {
			case 1:
				return sdk.ModelResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "lookup",
						Input:      toolexec.ArgumentsFromValue(map[string]any{"q": "one"}),
					}},
				}, nil
			case 2:
				secondCallMessages = append([]sdk.Message(nil), params.Messages...)
				return sdk.ModelResult{
					Text:         "ok",
					FinishReason: sdk.FinishReasonStop,
				}, nil
			default:
				return sdk.ModelResult{}, errors.New("unexpected provider call")
			}
		},
	}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{
			tools: []toolexec.Tool{{
				Name:       "lookup",
				Parameters: &jsonschema.Schema{Type: "object"},
				Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
					return toolexec.OutputFromValue(map[string]any{"answer": strings.Repeat("tool-result ", 64)}), nil
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

func TestAgentGenerateRecordsMidTaskPruneForProtectedRescue(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	modelProvider := &atomicMockProvider{
		handler: func(call int, _ sdk.Request) (sdk.ModelResult, error) {
			if call == 1 {
				return sdk.ModelResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "lookup",
						Input:      toolexec.ArgumentsFromValue(map[string]any{"q": "one"}),
					}},
				}, nil
			}
			return sdk.ModelResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
		},
	}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{
			tools: []toolexec.Tool{{
				Name:       "lookup",
				Parameters: &jsonschema.Schema{Type: "object"},
				Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
					return toolexec.OutputFromValue(map[string]any{"answer": strings.Repeat("tool-result ", 64)}), nil
				},
			}},
		},
	})

	_, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			return ContextStepSelectionResult{
				Messages:        append([]sdk.Message(nil), input.Messages...),
				Truncated:       1,
				ProtectedPruned: 1,
			}
		},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	var pruneDetail string
	for _, record := range ledger.Records() {
		if record.Kind == contextfrag.MutationMidTaskPrune {
			pruneDetail = record.Detail
		}
	}
	if pruneDetail != "truncated=1" {
		t.Fatalf("mutation records = %#v, want mid_task_prune with truncated=1", ledger.Records())
	}
}

func TestAgentGeneratePassesRemainingBudgetToStepReselector(t *testing.T) {
	t.Parallel()
	const budget = 1_000

	modelProvider := &atomicMockProvider{
		handler: func(call int, _ sdk.Request) (sdk.ModelResult, error) {
			if call == 1 {
				return sdk.ModelResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-budget",
						ToolName:   "lookup",
						Input:      toolexec.ArgumentsFromValue(map[string]any{"q": "one"}),
					}},
				}, nil
			}
			return sdk.ModelResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
		},
	}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{tools: []toolexec.Tool{{
			Name:       "lookup",
			Parameters: &jsonschema.Schema{Type: "object"},
			Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
				return toolexec.OutputFromValue(map[string]any{"answer": "ok"}), nil
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

	lookupTool := toolexec.Tool{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
			return toolexec.OutputFromValue(map[string]any{"answer": "ok"}), nil
		},
	}
	toolCost := contextfrag.ToolDefAccountingFor("native", lookupTool).TokenEstimate
	plan := contextfrag.ContextBudgetPlan{
		Window:        2048,
		OutputReserve: toolCost + 200,
	}

	var firstParams sdk.Request
	modelProvider := &atomicMockProvider{
		handler: func(call int, params sdk.Request) (sdk.ModelResult, error) {
			if call == 1 {
				firstParams = cloneGenerateParams(params)
				return sdk.ModelResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-budget-plan",
						ToolName:   "lookup",
						Input:      toolexec.ArgumentsFromValue(map[string]any{"q": "one"}),
					}},
				}, nil
			}
			return sdk.ModelResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
		},
	}

	a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
		return cfg, nil
	}})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{tools: []toolexec.Tool{lookupTool}},
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
		handler: func(call int, _ sdk.Request) (sdk.ModelResult, error) {
			if call != 1 {
				return sdk.ModelResult{}, fmt.Errorf("unexpected provider call %d after protected overflow", call)
			}
			return sdk.ModelResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls: []sdk.ToolCall{{
					ToolCallID: "call-step-overflow",
					ToolName:   "lookup",
					Input:      toolexec.ArgumentsFromValue(map[string]any{"q": "one"}),
				}},
			}, nil
		},
	}
	ledger := contextfrag.NewMutationLedger()
	a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
		return cfg, nil
	}})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{tools: []toolexec.Tool{{
			Name:       "lookup",
			Parameters: &jsonschema.Schema{Type: "object"},
			Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
				return toolexec.OutputFromValue(map[string]any{"answer": "ok"}), nil
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
		handler: func(call int, _ sdk.Request) (sdk.ModelResult, error) {
			if call != 1 {
				return sdk.ModelResult{}, fmt.Errorf("unexpected provider call %d after protected overflow", call)
			}
			return sdk.ModelResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls: []sdk.ToolCall{{
					ToolCallID: "call-stream-step-overflow",
					ToolName:   "lookup",
					Input:      toolexec.ArgumentsFromValue(map[string]any{"q": "one"}),
				}},
			}, nil
		},
	}
	a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
		return cfg, nil
	}})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{tools: []toolexec.Tool{{
			Name:       "lookup",
			Parameters: &jsonschema.Schema{Type: "object"},
			Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
				return toolexec.OutputFromValue(map[string]any{"answer": "ok"}), nil
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

// TestAgentGenerateStepRecordJoinsToolResultsWithCallInput pins the shape of a
// committed step's ToolResults: each entry pairs the originating call's Input
// with the loop's Output, which is what makes the field an toolexec.ToolResult
// rather than a bare result part. The lossless parts stay in Messages.
func TestAgentGenerateStepRecordJoinsToolResultsWithCallInput(t *testing.T) {
	t.Parallel()

	provider := &atomicMockProvider{
		handler: func(call int, _ sdk.Request) (sdk.ModelResult, error) {
			if call == 1 {
				return sdk.ModelResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "join-call",
						ToolName:   "join_tool",
						Input:      toolexec.ArgumentsFromValue(map[string]any{"city": "Kyoto"}),
					}},
				}, nil
			}
			return sdk.ModelResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
		},
	}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []toolexec.Tool{{
		Name: "join_tool",
		Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
			return toolexec.OutputFromValue("sunny"), nil
		},
	}}}})

	var records []step.Record
	if _, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "join-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("weather?")},
		SupportsToolCall: true,
		OnStepCommitted: func(_ context.Context, _ int, record *step.Record) (StepDirective, error) {
			records = append(records, *record)
			return StepDirective{}, nil
		},
	}); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("committed steps = %d, want 2", len(records))
	}
	results := records[0].ToolResults
	if len(results) != 1 {
		t.Fatalf("step 0 tool results = %#v, want exactly one", results)
	}
	if got := results[0].ToolCallID; got != "join-call" {
		t.Errorf("ToolCallID = %q, want join-call", got)
	}
	if got := results[0].ToolName; got != "join_tool" {
		t.Errorf("ToolName = %q, want join_tool", got)
	}
	if got, ok := toolexec.ArgumentsValue(results[0].Input).(map[string]any); !ok || got["city"] != "Kyoto" {
		t.Errorf("Input = %#v, want the originating call's input", results[0].Input)
	}
	if got := toolexec.OutputValue(results[0].Output); got != "sunny" {
		t.Errorf("Output = %#v, want sunny", got)
	}
}
