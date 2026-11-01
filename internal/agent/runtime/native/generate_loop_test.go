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

func (*atomicMockProvider) Test(context.Context) error {
	return nil
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

// A step request only grows: every later request extends the one before it
// byte for byte, so the results the run already sent stay whole however many
// steps it takes.
func TestAgentGenerateStepRequestsOnlyAppend(t *testing.T) {
	t.Parallel()

	const steps = 24
	result := strings.Repeat("tool-result ", 256)
	var requests [][]sdk.Message
	modelProvider := &atomicMockProvider{
		handler: func(call int, params sdk.Request) (sdk.ModelResult, error) {
			requests = append(requests, append([]sdk.Message(nil), params.Messages...))
			if call > steps {
				return sdk.ModelResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
			}
			return sdk.ModelResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls: []sdk.ToolCall{{
					ToolCallID: fmt.Sprintf("call-%d", call),
					ToolName:   "lookup",
					Input:      toolexec.ArgumentsFromValue(map[string]any{"q": call}),
				}},
			}, nil
		},
	}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{
			tools: []toolexec.Tool{{
				Name:       "lookup",
				Parameters: &jsonschema.Schema{Type: "object"},
				Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
					return toolexec.OutputFromValue(result), nil
				},
			}},
		},
	})

	ledger := contextfrag.NewMutationLedger()
	plan := contextfrag.ContextBudgetPlan{Window: 200_000, OutputReserve: 1000}
	_, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
		ContextManifest:  contextfrag.Manifest{BudgetPlan: &plan},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(requests) != steps+1 {
		t.Fatalf("provider calls = %d, want %d", len(requests), steps+1)
	}
	for i := 1; i < len(requests); i++ {
		prev, next := requests[i-1], requests[i]
		if len(next) != len(prev)+2 || !providerMessagesEqual(prev, next[:len(prev)]) {
			t.Fatalf("request %d does not extend request %d by one step", i, i-1)
		}
	}
	last := requests[len(requests)-1]
	for _, msg := range last {
		for _, part := range msg.Content {
			if tr, ok := part.(sdk.ToolResultPart); ok && toolexec.OutputValue(tr.Result) != result {
				t.Fatalf("tool result %s = %q, want the result as produced", tr.ToolCallID, tr.Result.String())
			}
		}
	}
	if records := ledger.Records(); len(records) != 0 {
		t.Fatalf("mutation records = %#v, want none", records)
	}
}

// A run that cannot compact in place ends when a step request no longer
// fits: nothing it already sent is dropped or rewritten to make room.
func TestAgentGenerateFailsClosedWhenStepRequestOutgrowsTheWindow(t *testing.T) {
	t.Parallel()

	modelProvider := &atomicMockProvider{
		handler: func(call int, _ sdk.Request) (sdk.ModelResult, error) {
			if call != 1 {
				return sdk.ModelResult{}, fmt.Errorf("unexpected provider call %d after the request outgrew the window", call)
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
	a.SetToolProviders([]agenttools.ToolProvider{oversizeLookupProvider()})

	plan := contextfrag.ContextBudgetPlan{Window: 2000, OutputReserve: 100}
	_, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
		ContextManifest:  contextfrag.Manifest{BudgetPlan: &plan},
	})
	if !errors.Is(err, contextfrag.ErrBudgetUnsatisfied) {
		t.Fatalf("Generate() error = %v, want %v", err, contextfrag.ErrBudgetUnsatisfied)
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
		records[0].Detail != "budget_unsatisfied" {
		t.Fatalf("budget failure mutations = %#v, want one budget_unsatisfied record", records)
	}
}

// oversizeLookupProvider serves a lookup tool whose result alone outgrows a
// 2000-token window.
func oversizeLookupProvider() agenttools.ToolProvider {
	return staticToolProvider{tools: []toolexec.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
			return toolexec.OutputFromValue(strings.Repeat("r", 20_000)), nil
		},
	}}}
}

func TestAgentStreamFailsClosedWhenStepRequestOutgrowsTheWindow(t *testing.T) {
	t.Parallel()

	modelProvider := &atomicMockProvider{
		handler: func(call int, _ sdk.Request) (sdk.ModelResult, error) {
			if call != 1 {
				return sdk.ModelResult{}, fmt.Errorf("unexpected provider call %d after the request outgrew the window", call)
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
	a.SetToolProviders([]agenttools.ToolProvider{oversizeLookupProvider()})

	plan := contextfrag.ContextBudgetPlan{Window: 2000, OutputReserve: 100}
	var errorEvents []StreamEvent
	for event := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		ContextManifest:  contextfrag.Manifest{BudgetPlan: &plan},
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
	if !errors.Is(errorEvents[0].Cause, contextfrag.ErrBudgetUnsatisfied) || errorEvents[0].Code != "" || errorEvents[0].Error != "" {
		t.Fatalf("error event = %#v, want only the budget sentinel as its cause", errorEvents[0])
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
