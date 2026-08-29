package native

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
)

func TestAgentStreamRecordsInjectedMessageMutation(t *testing.T) {
	t.Parallel()

	const marker = "injected between provider steps"
	injectCh := make(chan InjectMessage, 1)
	injectCh <- InjectMessage{Text: marker}

	var secondCall sdk.GenerateParams
	provider := &atomicMockProvider{handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
		if call == 1 {
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls:    []sdk.ToolCall{{ToolCallID: "inject-call", ToolName: "lookup"}},
			}, nil
		}
		secondCall = cloneGenerateParams(params)
		return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
	}}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(*sdk.ToolExecContext, any) (any, error) {
			return map[string]any{"ok": true}, nil
		},
	}}}})
	ledger := contextfrag.NewMutationLedger()
	type recordedInjection struct {
		text        string
		insertAfter int
	}
	var recorded []recordedInjection

	var terminal StreamEvent
	for event := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		InjectCh:         injectCh,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
		InjectedRecorder: func(text string, insertAfter int) {
			recorded = append(recorded, recordedInjection{text: text, insertAfter: insertAfter})
		},
	}) {
		if event.IsTerminal() {
			terminal = event
			if len(recorded) != 1 {
				t.Fatalf("recorded injections at terminal delivery = %#v, want one", recorded)
			}
		}
	}
	if !providerAttemptContainsText(secondCall.Messages, marker) {
		t.Fatalf("second provider call lost injected message: %#v", secondCall.Messages)
	}
	if !hasMutationKind(ledger.Records(), contextfrag.MutationInjectedMessage) {
		t.Fatalf("mutations = %#v, want %q", ledger.Records(), contextfrag.MutationInjectedMessage)
	}
	if terminal.Type != EventAgentEnd {
		t.Fatalf("terminal event = %q, want %q", terminal.Type, EventAgentEnd)
	}
	if len(recorded) != 1 || recorded[0].text != marker {
		t.Fatalf("recorded injections = %#v, want marker once", recorded)
	}
}

func TestAgentStreamDroppedInjectedMessageIsNotRecorded(t *testing.T) {
	t.Parallel()

	const marker = "drop this injected message"
	injectCh := make(chan InjectMessage, 1)
	injectCh <- InjectMessage{Text: marker}

	var secondCall sdk.GenerateParams
	provider := &atomicMockProvider{handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
		if call == 1 {
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls:    []sdk.ToolCall{{ToolCallID: "drop-inject", ToolName: "lookup"}},
			}, nil
		}
		secondCall = cloneGenerateParams(params)
		return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
	}}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(*sdk.ToolExecContext, any) (any, error) {
			return map[string]any{"ok": true}, nil
		},
	}}}})

	type recordedInjection struct {
		text        string
		insertAfter int
	}
	var recorded []recordedInjection
	var committed []sdk.StepResult
	for range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		InjectCh:         injectCh,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			selected := make([]sdk.Message, 0, len(input.Messages))
			dropped := 0
			for _, message := range input.Messages {
				if providerAttemptContainsText([]sdk.Message{message}, marker) {
					dropped++
					continue
				}
				selected = append(selected, message)
			}
			return ContextStepSelectionResult{Messages: selected, Dropped: dropped}
		},
		InjectedRecorder: func(text string, insertAfter int) {
			recorded = append(recorded, recordedInjection{text: text, insertAfter: insertAfter})
		},
		OnStepCommitted: func(_ context.Context, _ int, step *sdk.StepResult) error {
			committed = append(committed, *step)
			return nil
		},
	}) {
	}

	if providerAttemptContainsText(secondCall.Messages, marker) {
		t.Fatalf("second provider call retained dropped injection: %#v", secondCall.Messages)
	}
	if len(recorded) != 0 {
		t.Fatalf("recorded injections = %#v, want dropped injection revoked", recorded)
	}
	for i, step := range committed {
		if providerAttemptContainsText(step.Messages, marker) {
			t.Fatalf("committed step %d retained dropped injection: %#v", i, step.Messages)
		}
	}
}

func TestAgentStreamRetryRevokesInjectedMessageRecord(t *testing.T) {
	const marker = "retry-revoked injection"
	injectCh := make(chan InjectMessage, 1)
	injectCh <- InjectMessage{Text: marker}

	provider := &atomicMockProvider{handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
		switch call {
		case 1:
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls:    []sdk.ToolCall{{ToolCallID: "retry-inject", ToolName: "lookup"}},
			}, nil
		case 2:
			if !providerAttemptContainsText(params.Messages, marker) {
				t.Fatal("failed provider attempt did not receive admitted injection")
			}
			return nil, errors.New("api error 500")
		default:
			if providerAttemptContainsText(params.Messages, marker) {
				t.Fatal("retry provider attempt retained revoked injection")
			}
			return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
		}
	}}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(*sdk.ToolExecContext, any) (any, error) {
			return map[string]any{"ok": true}, nil
		},
	}}}})

	var selectorCalls atomic.Int32
	type recordedInjection struct {
		text        string
		insertAfter int
	}
	var recorded []recordedInjection
	var terminal StreamEvent
	for event := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		InjectCh:         injectCh,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			if selectorCalls.Add(1) == 1 {
				return ContextStepSelectionResult{Messages: input.Messages}
			}
			selected := make([]sdk.Message, 0, len(input.Messages))
			for _, message := range input.Messages {
				if !providerAttemptContainsText([]sdk.Message{message}, marker) {
					selected = append(selected, message)
				}
			}
			return ContextStepSelectionResult{
				Messages: selected,
				Dropped:  len(input.Messages) - len(selected),
			}
		},
		InjectedRecorder: func(text string, insertAfter int) {
			recorded = append(recorded, recordedInjection{text: text, insertAfter: insertAfter})
		},
	}) {
		if event.IsTerminal() {
			terminal = event
		}
	}

	if provider.calls.Load() != 3 || selectorCalls.Load() != 2 {
		t.Fatalf("provider/selector calls = %d/%d, want 3/2", provider.calls.Load(), selectorCalls.Load())
	}
	if terminal.Type != EventAgentEnd {
		t.Fatalf("terminal event = %q, want %q", terminal.Type, EventAgentEnd)
	}
	if len(recorded) != 0 {
		t.Fatalf("recorded injections = %#v, want retry-revoked injection omitted", recorded)
	}
}

func TestAgentStreamFailedPreflightDoesNotRecordInjectedMessage(t *testing.T) {
	t.Parallel()

	const marker = "rejected injected message"
	injectCh := make(chan InjectMessage, 1)
	injectCh <- InjectMessage{Text: marker}

	provider := &atomicMockProvider{handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
		if call != 1 {
			return nil, errors.New("provider called after failed injection preflight")
		}
		return &sdk.GenerateResult{
			FinishReason: sdk.FinishReasonToolCalls,
			ToolCalls:    []sdk.ToolCall{{ToolCallID: "reject-inject", ToolName: "lookup"}},
		}, nil
	}}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(*sdk.ToolExecContext, any) (any, error) {
			return map[string]any{"ok": true}, nil
		},
	}}}})

	var recorded []string
	var terminal StreamEvent
	for event := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		InjectCh:         injectCh,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			if !providerAttemptContainsText(input.Messages, marker) {
				t.Fatal("failed-preflight selector did not receive the injection")
			}
			return ContextStepSelectionResult{FatalError: contextfrag.ErrProtectedContextOverflow}
		},
		InjectedRecorder: func(text string, _ int) {
			recorded = append(recorded, text)
		},
	}) {
		if event.IsTerminal() {
			terminal = event
		}
	}

	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls.Load())
	}
	if terminal.Type != EventAgentAbort {
		t.Fatalf("terminal event = %q, want %q", terminal.Type, EventAgentAbort)
	}
	if len(recorded) != 0 {
		t.Fatalf("recorded injections = %#v, want failed-preflight injection omitted", recorded)
	}
}

func TestAgentStreamRecordsDuplicateAdmittedInjections(t *testing.T) {
	t.Parallel()

	const marker = "duplicate admitted injection"
	injectCh := make(chan InjectMessage, 2)
	injectCh <- InjectMessage{Text: marker}
	injectCh <- InjectMessage{Text: marker}

	provider := &atomicMockProvider{handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
		if call == 1 {
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls:    []sdk.ToolCall{{ToolCallID: "duplicate-inject", ToolName: "lookup"}},
			}, nil
		}
		raw, err := json.Marshal(params.Messages)
		if err != nil {
			t.Fatalf("marshal provider messages: %v", err)
		}
		if got := strings.Count(string(raw), marker); got != 2 {
			t.Fatalf("provider injection count = %d, want 2: %s", got, raw)
		}
		return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
	}}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(*sdk.ToolExecContext, any) (any, error) {
			return map[string]any{"ok": true}, nil
		},
	}}}})

	type recordedInjection struct {
		text        string
		insertAfter int
	}
	var recorded []recordedInjection
	for range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		InjectCh:         injectCh,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		InjectedRecorder: func(text string, insertAfter int) {
			recorded = append(recorded, recordedInjection{text: text, insertAfter: insertAfter})
		},
	}) {
	}

	if len(recorded) != 2 || recorded[0].text != marker || recorded[1].text != marker {
		t.Fatalf("recorded injections = %#v, want duplicate marker in arrival order", recorded)
	}
	if recorded[0].insertAfter != recorded[1].insertAfter {
		t.Fatalf("recorded insertion boundaries = %d/%d, want identical boundary for one PrepareStep", recorded[0].insertAfter, recorded[1].insertAfter)
	}
}

func TestAgentStreamRecordsLaterInjectionAtOutputBoundary(t *testing.T) {
	t.Parallel()

	const firstMarker = "first boundary injection"
	const secondMarker = "second boundary injection"
	injectCh := make(chan InjectMessage, 2)
	injectCh <- InjectMessage{Text: firstMarker}

	provider := &atomicMockProvider{handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
		switch call {
		case 1:
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls:    []sdk.ToolCall{{ToolCallID: "boundary-one", ToolName: "lookup"}},
			}, nil
		case 2:
			if !providerAttemptContainsText(params.Messages, firstMarker) {
				t.Fatal("second provider call lost first boundary injection")
			}
			injectCh <- InjectMessage{Text: secondMarker}
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls:    []sdk.ToolCall{{ToolCallID: "boundary-two", ToolName: "lookup"}},
			}, nil
		default:
			if !providerAttemptContainsText(params.Messages, secondMarker) {
				t.Fatal("third provider call lost second boundary injection")
			}
			return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
		}
	}}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(*sdk.ToolExecContext, any) (any, error) {
			return map[string]any{"ok": true}, nil
		},
	}}}})

	type recordedInjection struct {
		text        string
		insertAfter int
	}
	var recorded []recordedInjection
	for range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		InjectCh:         injectCh,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		InjectedRecorder: func(text string, insertAfter int) {
			recorded = append(recorded, recordedInjection{text: text, insertAfter: insertAfter})
		},
	}) {
	}

	if len(recorded) != 2 {
		t.Fatalf("recorded injections = %#v, want two", recorded)
	}
	if recorded[0] != (recordedInjection{text: firstMarker, insertAfter: 2}) {
		t.Fatalf("first injection = %#v, want boundary after first tool pair", recorded[0])
	}
	if recorded[1] != (recordedInjection{text: secondMarker, insertAfter: 4}) {
		t.Fatalf("second injection = %#v, want boundary after two tool pairs", recorded[1])
	}
}

func TestAgentStreamRecordsOnlyAdmittedDuplicateInjection(t *testing.T) {
	t.Parallel()

	const marker = "partially admitted duplicate injection"
	injectCh := make(chan InjectMessage, 2)
	injectCh <- InjectMessage{Text: marker}
	injectCh <- InjectMessage{Text: marker}

	provider := &atomicMockProvider{handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
		if call == 1 {
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls:    []sdk.ToolCall{{ToolCallID: "partial-duplicate", ToolName: "lookup"}},
			}, nil
		}
		if got := countRound8MessageText(params.Messages, marker); got != 1 {
			t.Fatalf("provider duplicate count = %d, want 1", got)
		}
		return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
	}}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(*sdk.ToolExecContext, any) (any, error) {
			return map[string]any{"ok": true}, nil
		},
	}}}})

	type recordedInjection struct {
		text        string
		insertAfter int
	}
	var recorded []recordedInjection
	for range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		InjectCh:         injectCh,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			selected := append([]sdk.Message(nil), input.Messages...)
			for i := len(selected) - 1; i >= input.InitialMessageCount; i-- {
				if providerAttemptContainsText([]sdk.Message{selected[i]}, marker) {
					selected = append(selected[:i], selected[i+1:]...)
					sourceIndexes := make([]int, 0, len(selected))
					for sourceIndex := range input.Messages {
						if sourceIndex != i {
							sourceIndexes = append(sourceIndexes, sourceIndex)
						}
					}
					return ContextStepSelectionResult{
						Messages:                  selected,
						MessageSourceIndexes:      sourceIndexes,
						MessageSourceIndexesKnown: true,
						Dropped:                   1,
					}
				}
			}
			t.Fatal("selector did not find duplicate injection")
			return ContextStepSelectionResult{}
		},
		InjectedRecorder: func(text string, insertAfter int) {
			recorded = append(recorded, recordedInjection{text: text, insertAfter: insertAfter})
		},
	}) {
	}

	if len(recorded) != 1 || recorded[0].text != marker {
		t.Fatalf("recorded injections = %#v, want exactly one admitted duplicate", recorded)
	}
	if recorded[0].insertAfter != 2 {
		t.Fatalf("recorded insertion boundary = %d, want after first tool pair", recorded[0].insertAfter)
	}
}
