package native

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/step"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

type agentStreamTestProvider func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error)

type streamEmitterCaptureProvider struct {
	emitter chan agenttools.StreamEmitter
}

func (p *streamEmitterCaptureProvider) Tools(_ context.Context, session agenttools.SessionContext) ([]toolexec.Tool, error) {
	p.emitter <- session.Emitter
	return nil, nil
}

func (agentStreamTestProvider) Name() string { return "stream-mock" }
func (agentStreamTestProvider) ListModels(context.Context) ([]sdk.Model, error) {
	return nil, nil
}

func (agentStreamTestProvider) Test(context.Context) *sdk.ProviderTestResult {
	return &sdk.ProviderTestResult{Status: sdk.ProviderStatusOK, Message: "ok"}
}

func (agentStreamTestProvider) TestModel(context.Context, string) (*sdk.ModelTestResult, error) {
	return &sdk.ModelTestResult{Supported: true, Message: "supported"}, nil
}

func (agentStreamTestProvider) DoGenerate(context.Context, sdk.Request) (sdk.ModelResult, error) {
	return sdk.ModelResult{FinishReason: sdk.FinishReasonStop}, nil
}

func (p agentStreamTestProvider) DoStream(ctx context.Context, params sdk.Request) (<-chan sdk.StreamPart, error) {
	return p(ctx, params)
}

func closedAgentTestStream(parts ...sdk.StreamPart) <-chan sdk.StreamPart {
	ch := make(chan sdk.StreamPart, len(parts))
	for _, part := range parts {
		ch <- part
	}
	close(ch)
	return ch
}

func finishedTextTestProvider(text string) agentStreamTestProvider {
	return func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
		return closedAgentTestStream(
			&sdk.StartStepPart{},
			&sdk.TextDeltaPart{ID: "text", Text: text},
			&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop},
		), nil
	}
}

func TestAgentStreamObservesReasoningEndBeforeStepCommit(t *testing.T) {
	t.Parallel()

	provider := agentStreamTestProvider(func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
		return closedAgentTestStream(
			&sdk.StartPart{},
			&sdk.StartStepPart{},
			&sdk.ReasoningStartPart{ID: "reasoning-1"},
			&sdk.ReasoningDeltaPart{ID: "reasoning-1", Text: "inspect"},
			&sdk.ReasoningEndPart{ID: "reasoning-1"},
			&sdk.TextDeltaPart{ID: "text-1", Text: "done"},
			&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop},
			&sdk.FinishPart{FinishReason: sdk.FinishReasonStop},
		), nil
	})

	var observed []StreamEventType
	commitSawReasoningEnd := false
	events := New(Deps{}).Stream(context.Background(), RunConfig{
		Model:    &sdk.Model{ID: "mock-model", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage("task")},
		Identity: SessionContext{BotID: "bot-1"},
		OnProviderStreamEventObserved: func(event StreamEvent) {
			observed = append(observed, event.Type)
		},
		OnStepCommitted: func(context.Context, int, *step.Record) (StepDirective, error) {
			commitSawReasoningEnd = slices.Contains(observed, EventReasoningEnd)
			return StepDirective{}, nil
		},
	})
	for range events {
	}

	if !commitSawReasoningEnd {
		t.Fatalf("step commit ran before reasoning_end observation: %#v", observed)
	}
}

func TestAgentStreamContinuesAfterFinalSteer(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var secondInput atomic.Bool
	var observedText atomic.Int32
	provider := agentStreamTestProvider(func(_ context.Context, params sdk.Request) (<-chan sdk.StreamPart, error) {
		call := calls.Add(1)
		if call == 2 {
			for _, message := range params.Messages {
				if strings.Contains(messageContentText(message), "change direction") {
					secondInput.Store(true)
				}
			}
		}
		return closedAgentTestStream(&sdk.StartPart{}, &sdk.StartStepPart{}, &sdk.TextDeltaPart{ID: "text", Text: "answer"}, &sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}, &sdk.FinishPart{FinishReason: sdk.FinishReasonStop}), nil
	})
	a := New(Deps{})
	var commits int
	events := a.Stream(context.Background(), RunConfig{
		Model:    &sdk.Model{ID: "mock-model", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage("hello")},
		Identity: SessionContext{BotID: "bot-1"},
		OnProviderStreamEventObserved: func(event StreamEvent) {
			if event.Type == EventTextDelta {
				observedText.Add(1)
			}
		},
		OnStepCommitted: func(_ context.Context, _ int, _ *step.Record) (StepDirective, error) {
			commits++
			if commits == 1 {
				return StepDirective{NextInputs: []DirectiveInput{{ID: "steer-1", Text: "change direction"}}}, nil
			}
			return StepDirective{}, nil
		},
	})
	var terminal, starts int
	var steps []int
	for event := range events {
		if event.Type == EventAgentStart {
			starts++
		}
		if event.Type == EventStepEnd {
			steps = append(steps, event.StepNumber)
		}
		if event.IsTerminal() {
			terminal++
		}
	}
	if observedText.Load() != 2 || starts != 1 || len(steps) != 2 || steps[0] != 0 || steps[1] != 1 {
		t.Fatalf("duplicate observer/start or wrong step offset: observed=%d starts=%d steps=%v", observedText.Load(), starts, steps)
	}
	if calls.Load() != 2 || !secondInput.Load() || terminal != 1 {
		t.Fatalf("calls=%d second_input=%v terminal=%d", calls.Load(), secondInput.Load(), terminal)
	}
}

func TestAgentStreamPersistsDirectiveInputOnceWithStepOffset(t *testing.T) {
	t.Parallel()
	const marker = "offset continuation steer"
	var calls atomic.Int32
	provider := agentStreamTestProvider(func(_ context.Context, params sdk.Request) (<-chan sdk.StreamPart, error) {
		call := calls.Add(1)
		if call == 2 && !providerAttemptContainsText(params.Messages, marker) {
			t.Fatalf("second provider call lost offset steer: %#v", params.Messages)
		}
		return closedAgentTestStream(&sdk.StartPart{}, &sdk.StartStepPart{}, &sdk.TextDeltaPart{ID: "text", Text: "answer"}, &sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}, &sdk.FinishPart{FinishReason: sdk.FinishReasonStop}), nil
	})
	committedByIndex := map[int][]sdk.Message{}
	events := New(Deps{}).Stream(context.Background(), RunConfig{
		Model:           &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:        []sdk.Message{sdk.UserMessage("hello")},
		Identity:        SessionContext{BotID: "bot-1"},
		StepIndexOffset: 2,
		OnStepCommitted: func(_ context.Context, stepIndex int, step *step.Record) (StepDirective, error) {
			committedByIndex[stepIndex] = append([]sdk.Message(nil), step.Messages...)
			if stepIndex == 2 {
				return StepDirective{NextInputs: []DirectiveInput{{Text: marker}}}, nil
			}
			return StepDirective{}, nil
		},
	})
	for range events {
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d, want 2", calls.Load())
	}
	second := committedByIndex[3]
	if !providerAttemptContainsText(second, marker) {
		t.Fatalf("committed step 3 = %#v, want steer persisted once", second)
	}
	if providerAttemptContainsText(committedByIndex[2], marker) {
		t.Fatalf("committed step 2 included steer: %#v", committedByIndex[2])
	}
}

func messageContentText(message sdk.Message) string {
	var out strings.Builder
	for _, part := range message.Content {
		if text, ok := part.(sdk.TextPart); ok {
			out.WriteString(text.Text)
		}
	}
	return out.String()
}

// TestAgentStreamEmitsToolCallInputStartThenStart asserts that a tool call
// produces a lightweight EventToolCallInputStart (name + call ID, no input)
// when the SDK emits ToolInputStartPart, followed by a EventToolCallStart
// carrying the fully-assembled Input when StreamToolCallPart arrives. The
// early input-start lets the Web UI render the tool block while arguments are
// still streaming, while IM adapters (which do not map input-start) keep their
// single-start behavior and avoid duplicate "running" messages.
func TestAgentStreamEmitsToolCallInputStartThenStart(t *testing.T) {
	t.Parallel()

	a := New(Deps{})
	provider := agentStreamTestProvider(func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
		return closedAgentTestStream(
			&sdk.StartPart{}, &sdk.StartStepPart{},
			&sdk.ToolInputStartPart{ID: "call-1", ToolName: "write"},
			&sdk.StreamToolCallPart{ToolCallID: "call-1", ToolName: "write", Input: toolexec.ArgumentsFromValue(map[string]any{"path": "/tmp/long.txt"})},
			&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop},
			&sdk.FinishPart{FinishReason: sdk.FinishReasonStop},
		), nil
	})

	var events []StreamEvent
	commits := 0
	for event := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("write a long file")},
		SupportsToolCall: false,
		Identity:         SessionContext{BotID: "bot-1"},
		OnStepCommitted: func(context.Context, int, *step.Record) (StepDirective, error) {
			commits++
			return StepDirective{}, nil
		},
	}) {
		events = append(events, event)
	}

	if len(events) != 5 {
		t.Fatalf("expected 5 events, got %d: %#v", len(events), events)
	}
	if events[0].Type != EventAgentStart {
		t.Fatalf("expected first event %q, got %#v", EventAgentStart, events[0])
	}
	// step_end follows every part of the step and precedes the terminal event;
	// it carries the durable step index the following commit uses.
	if events[3].Type != EventStepEnd || events[3].StepNumber != 0 {
		t.Fatalf("expected step_end for step 0 before agent_end, got %#v", events[3])
	}
	if events[1].Type != EventToolCallInputStart || events[1].ToolCallID != "call-1" || events[1].ToolName != "write" {
		t.Fatalf("unexpected tool call input start event: %#v", events[1])
	}
	if events[1].Input != nil {
		t.Fatalf("expected tool call input start to carry no input, got %#v", events[1].Input)
	}
	if events[2].Type != EventToolCallStart || events[2].ToolCallID != "call-1" || events[2].ToolName != "write" {
		t.Fatalf("unexpected tool call start event: %#v", events[2])
	}
	expectedInput := map[string]any{"path": "/tmp/long.txt"}
	if !reflect.DeepEqual(events[2].Input, expectedInput) {
		t.Fatalf("expected tool call start input %#v, got %#v", expectedInput, events[2].Input)
	}
	if events[4].Type != EventAgentEnd {
		t.Fatalf("expected terminal event %q, got %#v", EventAgentEnd, events[4])
	}
	if commits != 1 {
		t.Fatalf("committed steps = %d, want 1", commits)
	}
}

func TestAgentStreamCancellationDoesNotWaitForProviderToClose(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	provider := agentStreamTestProvider(func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
		return make(chan sdk.StreamPart), nil
	})
	events := New(Deps{}).Stream(ctx, RunConfig{
		Model:    &sdk.Model{ID: "mock-model", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage("keep streaming")},
		Identity: SessionContext{BotID: "bot-1"},
	})

	first := <-events
	if first.Type != EventAgentStart {
		t.Fatalf("first event = %q, want %q", first.Type, EventAgentStart)
	}
	cancel()

	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("stream closed without an abort event")
		}
		if event.Type != EventAgentAbort {
			t.Fatalf("terminal event = %q, want %q", event.Type, EventAgentAbort)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after cancellation")
	}
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("stream emitted an event after its terminal abort")
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not close after its terminal abort")
	}
}

func TestStreamEmitterGateRejectsLateEventsAndWaitsForInFlightSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan StreamEvent)
	gate := newStreamEmitterGate(ctx, ch)

	sendDone := make(chan struct{})
	go func() {
		gate.emit(agenttools.ToolStreamEvent{Type: agenttools.StreamEventSpawnProgress})
		close(sendDone)
	}()

	select {
	case <-sendDone:
		t.Fatal("emitter returned before a receiver or cancellation")
	case <-time.After(10 * time.Millisecond):
	}

	cancel()
	gate.close()
	select {
	case <-sendDone:
	case <-time.After(time.Second):
		t.Fatal("gate did not wait for in-flight emitter")
	}

	gate.emit(agenttools.ToolStreamEvent{Type: agenttools.StreamEventSpawnProgress})
	close(ch)
}

func TestAgentStreamRejectsCapturedEmitterAfterClose(t *testing.T) {
	capture := &streamEmitterCaptureProvider{emitter: make(chan agenttools.StreamEmitter, 1)}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{capture})

	var terminal StreamEvent
	for event := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: finishedTextTestProvider("done")},
		Messages:         []sdk.Message{sdk.UserMessage("finish normally")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
	}) {
		if event.IsTerminal() {
			terminal = event
		}
	}
	if terminal.Type != EventAgentEnd {
		t.Fatalf("terminal event = %q, want %q", terminal.Type, EventAgentEnd)
	}

	emitter := <-capture.emitter
	if emitter == nil {
		t.Fatal("captured emitter is nil")
	}
	emitDone := make(chan struct{})
	go func() {
		emitter(agenttools.ToolStreamEvent{Type: agenttools.StreamEventSpawnProgress})
		close(emitDone)
	}()
	select {
	case <-emitDone:
	case <-time.After(time.Second):
		t.Fatal("captured emitter blocked after stream close")
	}
}

func TestSpawnProgressPreservesWireStatus(t *testing.T) {
	event := toolStreamEventToAgentEvent(agenttools.ToolStreamEvent{Type: agenttools.StreamEventSpawnProgress})
	if event.Type != EventProgress || event.ProgressStatus != "spawn_running" {
		t.Fatalf("spawn progress event = %#v, want progress/spawn_running", event)
	}
}

func TestAgentStreamPersistsInterruptedInferenceStep(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := agentStreamTestProvider(func(ctx context.Context, _ sdk.Request) (<-chan sdk.StreamPart, error) {
		ch := make(chan sdk.StreamPart, 3)
		ch <- &sdk.StartStepPart{}
		ch <- &sdk.ReasoningDeltaPart{ID: "reasoning", Text: "thinking"}
		ch <- &sdk.TextDeltaPart{ID: "text", Text: "partial"}
		go func() { <-ctx.Done(); close(ch) }()
		return ch, nil
	})
	var interrupted *step.Record
	events := New(Deps{}).Stream(ctx, RunConfig{
		Model:    &sdk.Model{ID: "mock-model", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage("keep streaming")},
		Identity: SessionContext{BotID: "bot-1"},
		OnStepInterrupted: func(callbackCtx context.Context, stepIndex int, record *step.Record) error {
			if !errors.Is(context.Cause(callbackCtx), context.Canceled) || stepIndex != 0 {
				t.Errorf("callback context/index = %v/%d", callbackCtx.Err(), stepIndex)
			}
			interrupted = record
			return nil
		},
	})
	var terminal StreamEvent
	for event := range events {
		if event.Type == EventTextDelta {
			cancel()
		}
		if event.IsTerminal() {
			terminal = event
		}
	}
	if interrupted == nil || interrupted.Result.Reasoning != "thinking" || interrupted.Result.Text != "partial" {
		t.Fatalf("interrupted step = %#v", interrupted)
	}
	var messages []sdk.Message
	if terminal.Type != EventAgentAbort || json.Unmarshal(terminal.Messages, &messages) != nil || len(messages) != 1 {
		t.Fatalf("terminal event/messages = %#v / %#v", terminal, messages)
	}
}

func TestInterruptedStepCaptureRejectsToolStep(t *testing.T) {
	var capture interruptedStepCapture
	capture.observe(&sdk.TextDeltaPart{Text: "partial"})
	capture.observe(&sdk.ToolInputStartPart{ID: "call", ToolName: "exec"})
	if step := capture.snapshot(0); step != nil {
		t.Fatalf("snapshot after tool activity = %#v, want nil", step)
	}
}

func TestInterruptedStepCaptureRejectsAlreadyCommittedStep(t *testing.T) {
	var capture interruptedStepCapture
	capture.observe(&sdk.StartStepPart{})
	capture.observe(&sdk.TextDeltaPart{Text: "partial"})
	if step := capture.snapshot(1); step != nil {
		t.Fatalf("snapshot of a step the SDK already committed = %#v, want nil", step)
	}
	if step := capture.snapshot(0); step == nil {
		t.Fatal("snapshot of the uncommitted frontier = nil, want the partial text")
	}
	capture.observe(&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop})
	if step := capture.snapshot(0); step == nil {
		t.Fatal("finished step whose complete commit failed = nil, want interrupted fallback")
	}
}

func TestAgentStreamDoesNotCheckpointAnAlreadyCommittedStep(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	committed := make(chan string, 4)
	interrupted := make(chan string, 4)
	events := New(Deps{}).Stream(ctx, RunConfig{
		Model:    &sdk.Model{ID: "mock-model", Provider: finishedTextTestProvider("final answer")},
		Messages: []sdk.Message{sdk.UserMessage("hi")},
		Identity: SessionContext{BotID: "bot-1"},
		OnStepCommitted: func(_ context.Context, _ int, step *step.Record) (StepDirective, error) {
			committed <- step.Result.Text
			return StepDirective{}, nil
		},
		OnStepInterrupted: func(_ context.Context, _ int, step *step.Record) error {
			interrupted <- step.Result.Text
			return nil
		},
	})

	// Take only agent_start so the event loop parks on the text delta it has
	// already observed, leaving the finish-step part unread.
	if first, ok := <-events; !ok || first.Type != EventAgentStart {
		t.Fatalf("first event = %#v", first)
	}
	select {
	case <-committed:
	case <-time.After(5 * time.Second):
		t.Fatal("step was never committed")
	}

	cancel()
	for range events {
	}

	select {
	case text := <-interrupted:
		t.Fatalf("checkpointed %q, which the complete-step barrier already made durable", text)
	default:
	}
}

func TestAgentStreamCheckpointsWhenCompleteCommitLosesAbortRace(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	commitStarted := make(chan struct{})
	interrupted := make(chan string, 1)
	events := New(Deps{}).Stream(ctx, RunConfig{
		Model:    &sdk.Model{ID: "mock-model", Provider: finishedTextTestProvider("final answer")},
		Messages: []sdk.Message{sdk.UserMessage("hi")},
		Identity: SessionContext{BotID: "bot-1"},
		OnStepCommitted: func(context.Context, int, *step.Record) (StepDirective, error) {
			close(commitStarted)
			<-ctx.Done()
			return StepDirective{}, errors.New("abort won")
		},
		OnStepInterrupted: func(_ context.Context, _ int, step *step.Record) error {
			interrupted <- step.Result.Text
			return nil
		},
	})

	if first := <-events; first.Type != EventAgentStart {
		t.Fatalf("first event = %#v", first)
	}
	<-commitStarted
	cancel()
	for range events {
	}
	select {
	case text := <-interrupted:
		if text != "final answer" {
			t.Fatalf("interrupted text = %q", text)
		}
	default:
		t.Fatal("complete step rejected by abort was not checkpointed")
	}
}
