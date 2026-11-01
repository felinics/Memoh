package native

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/step"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

func TestAgentStreamProviderStartFailureDoesNotPersistInjectedMessage(t *testing.T) {
	t.Parallel()

	const marker = "injection observed by failed provider start"
	injectCh := make(chan InjectMessage, 1)
	injectCh <- InjectMessage{Text: marker}

	provider := &atomicMockProvider{handler: func(call int, params sdk.Request) (sdk.ModelResult, error) {
		switch call {
		case 1:
			return sdk.ModelResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls:    []sdk.ToolCall{{ToolCallID: "start-failure", ToolName: "lookup"}},
			}, nil
		case 2:
			if !providerAttemptContainsText(params.Messages, marker) {
				t.Fatal("failed provider start did not receive the injected message")
			}
			return sdk.ModelResult{}, errors.New("invalid provider request")
		default:
			t.Fatalf("provider call %d crossed the nonretryable start failure", call)
			return sdk.ModelResult{}, nil
		}
	}}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []toolexec.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
			return toolexec.OutputFromValue(map[string]any{"ok": true}), nil
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

func TestAgentStreamCanceledRetryDoesNotPersistUncommittedInjection(t *testing.T) {
	t.Parallel()

	const marker = "injection observed before the retry was canceled"
	injectCh := make(chan InjectMessage, 1)
	injectCh <- InjectMessage{Text: marker}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	provider := &atomicMockProvider{handler: func(call int, params sdk.Request) (sdk.ModelResult, error) {
		switch call {
		case 1:
			return sdk.ModelResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls:    []sdk.ToolCall{{ToolCallID: "retry-fence", ToolName: "lookup"}},
			}, nil
		case 2:
			if !providerAttemptContainsText(params.Messages, marker) {
				t.Fatal("failed dispatched attempt did not receive the injected message")
			}
			cancel()
			return sdk.ModelResult{}, serverErr()
		default:
			t.Fatalf("provider call %d crossed the canceled retry", call)
			return sdk.ModelResult{}, nil
		}
	}}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []toolexec.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
			return toolexec.OutputFromValue(map[string]any{"ok": true}), nil
		},
	}}}})

	var recorded []string
	for range a.Stream(ctx, RunConfig{
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
	}

	if provider.calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls.Load())
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
	provider := &atomicMockProvider{stream: func(streamCtx context.Context, params sdk.Request) (<-chan sdk.StreamPart, error) {
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
			return parts, nil
		}
	}}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []toolexec.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
			return toolexec.OutputFromValue(map[string]any{"ok": true}), nil
		},
	}}}})

	var interrupted *step.Record
	var recorded []string
	events := a.Stream(ctx, RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		InjectCh:         injectCh,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		OnStepInterrupted: func(_ context.Context, stepIndex int, step *step.Record) error {
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
