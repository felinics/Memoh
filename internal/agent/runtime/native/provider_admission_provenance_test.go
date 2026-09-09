package native

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
)

func TestAgentStreamRecordsLaterDuplicateInjectionRetainedByReselection(t *testing.T) {
	t.Parallel()

	const marker = "duplicate injection across provider steps"
	injectCh := make(chan InjectMessage, 1)
	injectCh <- InjectMessage{Text: marker}

	provider := &atomicMockProvider{handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
		switch call {
		case 1:
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls:    []sdk.ToolCall{{ToolCallID: "duplicate-step-one", ToolName: "lookup"}},
			}, nil
		case 2:
			if got := countRound8MessageText(params.Messages, marker); got != 1 {
				t.Fatalf("second provider duplicate count = %d, want 1", got)
			}
			injectCh <- InjectMessage{Text: marker}
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls:    []sdk.ToolCall{{ToolCallID: "duplicate-step-two", ToolName: "lookup"}},
			}, nil
		default:
			if got := countRound8MessageText(params.Messages, marker); got != 1 {
				t.Fatalf("third provider duplicate count = %d, want latest copy only", got)
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
	var selectorCalls int
	for range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		InjectCh:         injectCh,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			selectorCalls++
			if selectorCalls == 1 {
				return ContextStepSelectionResult{}
			}
			latest := -1
			for i := len(input.Messages) - 1; i >= input.InitialMessageCount; i-- {
				if providerAttemptContainsText([]sdk.Message{input.Messages[i]}, marker) {
					latest = i
					break
				}
			}
			if latest < 0 {
				t.Fatal("selector did not find the latest duplicate injection")
			}
			selected := append([]sdk.Message(nil), input.Messages[:input.InitialMessageCount]...)
			selected = append(selected,
				sdk.Message{Role: sdk.MessageRoleSystem, Content: []sdk.MessagePart{sdk.TextPart{Text: "history trimmed"}}},
				input.Messages[latest],
			)
			sourceIndexes := make([]int, 0, input.InitialMessageCount+2)
			for i := 0; i < input.InitialMessageCount; i++ {
				sourceIndexes = append(sourceIndexes, i)
			}
			sourceIndexes = append(sourceIndexes, -1, latest)
			return ContextStepSelectionResult{
				Messages:                  selected,
				MessageSourceIndexes:      sourceIndexes,
				MessageSourceIndexesKnown: true,
				Dropped:                   len(input.Messages) - input.InitialMessageCount - 1,
			}
		},
		InjectedRecorder: func(text string, insertAfter int) {
			recorded = append(recorded, recordedInjection{text: text, insertAfter: insertAfter})
		},
	}) {
	}

	want := []recordedInjection{
		{text: marker, insertAfter: 2},
		{text: marker, insertAfter: 4},
	}
	if !reflect.DeepEqual(recorded, want) {
		t.Fatalf("recorded injections = %#v, want %#v", recorded, want)
	}
}

func TestAgentStreamProviderStartFailureDoesNotPersistInjectedMessage(t *testing.T) {
	t.Parallel()

	const marker = "injection observed by failed provider start"
	injectCh := make(chan InjectMessage, 1)
	injectCh <- InjectMessage{Text: marker}

	provider := &atomicMockProvider{handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
		switch call {
		case 1:
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls:    []sdk.ToolCall{{ToolCallID: "start-failure", ToolName: "lookup"}},
			}, nil
		case 2:
			if !providerAttemptContainsText(params.Messages, marker) {
				t.Fatal("failed provider start did not receive the injected message")
			}
			return nil, errors.New("invalid provider request")
		default:
			t.Fatalf("provider call %d crossed the nonretryable start failure", call)
			return nil, nil
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

	var recorded []string
	var terminal StreamEvent
	for event := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		InjectCh:         injectCh,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		InjectedRecorder: func(text string, _ int) {
			recorded = append(recorded, text)
		},
	}) {
		if event.IsTerminal() {
			terminal = event
		}
	}

	if provider.calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls.Load())
	}
	if len(recorded) != 0 {
		t.Fatalf("recorded injections = %#v, want no message without a completed step", recorded)
	}
	var terminalMessages []sdk.Message
	if !terminal.IsTerminal() || json.Unmarshal(terminal.Messages, &terminalMessages) != nil {
		t.Fatalf("terminal event/messages = %#v / %#v", terminal, terminalMessages)
	}
	if providerAttemptContainsText(terminalMessages, marker) {
		t.Fatalf("terminal messages persisted failed-start injection: %#v", terminalMessages)
	}
}

func TestAgentStreamRetryPreflightFailureDoesNotPersistUncommittedInjection(t *testing.T) {
	t.Parallel()

	const marker = "injection observed before retry preflight failure"
	injectCh := make(chan InjectMessage, 1)
	injectCh <- InjectMessage{Text: marker}

	provider := &atomicMockProvider{handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
		switch call {
		case 1:
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls:    []sdk.ToolCall{{ToolCallID: "retry-fence", ToolName: "lookup"}},
			}, nil
		case 2:
			if !providerAttemptContainsText(params.Messages, marker) {
				t.Fatal("failed dispatched attempt did not receive the injected message")
			}
			return nil, errors.New("api error 500")
		default:
			t.Fatalf("provider call %d crossed the failed retry preflight", call)
			return nil, nil
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

	var selectorCalls int
	var recorded []string
	for range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		InjectCh:         injectCh,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		ContextStepReselector: func(_ context.Context, _ ContextStepSelectionInput) ContextStepSelectionResult {
			selectorCalls++
			if selectorCalls == 1 {
				return ContextStepSelectionResult{}
			}
			return ContextStepSelectionResult{FatalError: contextfrag.ErrProtectedContextOverflow}
		},
		InjectedRecorder: func(text string, _ int) {
			recorded = append(recorded, text)
		},
	}) {
	}

	if provider.calls.Load() != 2 || selectorCalls != 2 {
		t.Fatalf("provider/selector calls = %d/%d, want 2/2", provider.calls.Load(), selectorCalls)
	}
	if len(recorded) != 0 {
		t.Fatalf("recorded injections = %#v, want no message without a completed step", recorded)
	}
}

func TestAgentStreamInterruptedInjectedMessageIsDurableExactlyOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const marker = "interrupted injected message"
	injectCh := make(chan InjectMessage, 1)
	injectCh <- InjectMessage{Text: marker}

	var calls int
	provider := &atomicMockProvider{stream: func(streamCtx context.Context, params sdk.GenerateParams) (*sdk.StreamResult, error) {
		calls++
		switch calls {
		case 1:
			return closedAgentTestStream(
				&sdk.StartPart{},
				&sdk.StartStepPart{},
				&sdk.StreamToolCallPart{ToolCallID: "interrupt-inject", ToolName: "lookup"},
				&sdk.FinishStepPart{FinishReason: sdk.FinishReasonToolCalls},
				&sdk.FinishPart{FinishReason: sdk.FinishReasonToolCalls},
			), nil
		default:
			if got := countRound8MessageText(params.Messages, marker); got != 1 {
				t.Fatalf("interrupted provider marker count = %d, want 1", got)
			}
			parts := make(chan sdk.StreamPart, 4)
			parts <- &sdk.StartPart{}
			parts <- &sdk.StartStepPart{}
			parts <- &sdk.TextStartPart{ID: "partial"}
			parts <- &sdk.TextDeltaPart{ID: "partial", Text: "partial answer"}
			go func() {
				<-streamCtx.Done()
				close(parts)
			}()
			return &sdk.StreamResult{Stream: parts}, nil
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

	var interrupted *sdk.StepResult
	var recorded []string
	events := a.Stream(ctx, RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		InjectCh:         injectCh,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		OnStepInterrupted: func(_ context.Context, stepIndex int, step *sdk.StepResult) error {
			if stepIndex != 1 {
				t.Errorf("interrupted step index = %d, want 1", stepIndex)
			}
			interrupted = step
			return nil
		},
		InjectedRecorder: func(text string, _ int) {
			recorded = append(recorded, text)
		},
	})

	var terminal StreamEvent
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				goto streamClosed
			}
			if event.Type == EventTextDelta {
				cancel()
			}
			if event.IsTerminal() {
				terminal = event
			}
		case <-timer.C:
			t.Fatal("timed out waiting for interrupted injected-message stream")
		}
	}

streamClosed:
	if interrupted == nil {
		t.Fatal("interrupted step was not persisted")
	}
	if countRound8MessageText(interrupted.Messages, marker) != 1 {
		t.Fatalf("interrupted marker count = %d, want 1", countRound8MessageText(interrupted.Messages, marker))
	}
	var terminalMessages []sdk.Message
	if terminal.Type != EventAgentAbort || json.Unmarshal(terminal.Messages, &terminalMessages) != nil {
		t.Fatalf("terminal event/messages = %#v / %#v", terminal, terminalMessages)
	}
	if got := countRound8MessageText(terminalMessages, marker); got != 1 {
		t.Fatalf("terminal marker count = %d, want 1", got)
	}
	if len(recorded) != 0 {
		t.Fatalf("recorded injections = %#v, want interrupted checkpoint ownership", recorded)
	}
}

func TestSelectionMessageSourceIndexesRequiresVerifiableOrigins(t *testing.T) {
	t.Parallel()

	before := []sdk.Message{
		sdk.UserMessage("fixed prefix"),
		sdk.AssistantMessage("first suffix"),
		sdk.UserMessage("second suffix"),
	}
	cachedAll := cloneProviderMessages(before)
	part := cachedAll[1].Content[0].(sdk.TextPart)
	part.CacheControl = &sdk.CacheControl{Type: "ephemeral"}
	cachedAll[1].Content[0] = part
	cachedPrefix := cachedAll[:2]

	tests := []struct {
		name      string
		after     []sdk.Message
		selection ContextStepSelectionResult
		want      []int
		valid     bool
	}{
		{
			name:  "unknown changed normalized prefix",
			after: cachedPrefix,
			want:  []int{0, -1},
			valid: true,
		},
		{
			name:  "unknown changed subsequence",
			after: []sdk.Message{before[0], before[2]},
			want:  []int{0, -1},
			valid: true,
		},
		{
			name:  "unknown normalized unchanged",
			after: cachedAll,
			want:  []int{0, 1, 2},
			valid: true,
		},
		{
			name:  "known normalized cache rewrite",
			after: cachedPrefix,
			selection: ContextStepSelectionResult{
				MessageSourceIndexes:      []int{0, 1},
				MessageSourceIndexesKnown: true,
			},
			want:  []int{0, 1},
			valid: true,
		},
		{
			name:  "known source content mismatch",
			after: []sdk.Message{before[0], before[2]},
			selection: ContextStepSelectionResult{
				MessageSourceIndexes:      []int{0, 1},
				MessageSourceIndexesKnown: true,
			},
			want:  []int{0, -1},
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, valid := selectionMessageSourceIndexes(before, tt.after, 1, tt.selection)
			if valid != tt.valid || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("source indexes = %#v, %t; want %#v, %t", got, valid, tt.want, tt.valid)
			}
		})
	}
}
