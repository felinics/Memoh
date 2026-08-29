package native

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	agenttools "github.com/felinics/memoh/internal/agent/tool"
)

type agentStreamTestProvider func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error)

type streamEmitterCaptureProvider struct {
	emitter chan agenttools.StreamEmitter
}

func (p *streamEmitterCaptureProvider) Tools(_ context.Context, session agenttools.SessionContext) ([]sdk.Tool, error) {
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

func (agentStreamTestProvider) DoGenerate(context.Context, sdk.GenerateParams) (*sdk.GenerateResult, error) {
	return &sdk.GenerateResult{FinishReason: sdk.FinishReasonStop}, nil
}

func (p agentStreamTestProvider) DoStream(ctx context.Context, params sdk.GenerateParams) (*sdk.StreamResult, error) {
	return p(ctx, params)
}

func closedAgentTestStream(parts ...sdk.StreamPart) *sdk.StreamResult {
	ch := make(chan sdk.StreamPart, len(parts))
	for _, part := range parts {
		ch <- part
	}
	close(ch)
	return &sdk.StreamResult{Stream: ch}
}

func finishedTextTestProvider(text string) agentStreamTestProvider {
	return func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
		return closedAgentTestStream(
			&sdk.StartStepPart{},
			&sdk.TextDeltaPart{ID: "text", Text: text},
			&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop},
		), nil
	}
}

func TestAgentStreamObservesReasoningEndBeforeStepCommit(t *testing.T) {
	t.Parallel()

	provider := agentStreamTestProvider(func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
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
		OnStepCommitted: func(context.Context, int, *sdk.StepResult) error {
			commitSawReasoningEnd = slices.Contains(observed, EventReasoningEnd)
			return nil
		},
	})
	for range events {
	}

	if !commitSawReasoningEnd {
		t.Fatalf("step commit ran before reasoning_end observation: %#v", observed)
	}
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
	provider := agentStreamTestProvider(func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
		return closedAgentTestStream(
			&sdk.StartPart{}, &sdk.StartStepPart{},
			&sdk.ToolInputStartPart{ID: "call-1", ToolName: "write"},
			&sdk.StreamToolCallPart{ToolCallID: "call-1", ToolName: "write", Input: map[string]any{"path": "/tmp/long.txt"}},
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
		OnStepCommitted: func(context.Context, int, *sdk.StepResult) error {
			commits++
			return nil
		},
	}) {
		events = append(events, event)
	}

	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %#v", len(events), events)
	}
	if events[0].Type != EventAgentStart {
		t.Fatalf("expected first event %q, got %#v", EventAgentStart, events[0])
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
	if events[3].Type != EventAgentEnd {
		t.Fatalf("expected terminal event %q, got %#v", EventAgentEnd, events[3])
	}
	if commits != 1 {
		t.Fatalf("committed steps = %d, want 1", commits)
	}
}

func TestAgentStreamCancellationDoesNotWaitForProviderToClose(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	provider := agentStreamTestProvider(func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
		return &sdk.StreamResult{Stream: make(chan sdk.StreamPart)}, nil
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
	provider := agentStreamTestProvider(func(ctx context.Context, _ sdk.GenerateParams) (*sdk.StreamResult, error) {
		ch := make(chan sdk.StreamPart, 3)
		ch <- &sdk.StartStepPart{}
		ch <- &sdk.ReasoningDeltaPart{ID: "reasoning", Text: "thinking"}
		ch <- &sdk.TextDeltaPart{ID: "text", Text: "partial"}
		go func() { <-ctx.Done(); close(ch) }()
		return &sdk.StreamResult{Stream: ch}, nil
	})
	var interrupted *sdk.StepResult
	events := New(Deps{}).Stream(ctx, RunConfig{
		Model:    &sdk.Model{ID: "mock-model", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage("keep streaming")},
		Identity: SessionContext{BotID: "bot-1"},
		OnStepInterrupted: func(callbackCtx context.Context, stepIndex int, step *sdk.StepResult) error {
			if callbackCtx.Err() != nil || stepIndex != 0 {
				t.Errorf("callback context/index = %v/%d", callbackCtx.Err(), stepIndex)
			}
			interrupted = step
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
	if interrupted == nil || interrupted.Reasoning != "thinking" || interrupted.Text != "partial" {
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
		OnStepCommitted: func(_ context.Context, _ int, step *sdk.StepResult) error {
			committed <- step.Text
			return nil
		},
		OnStepInterrupted: func(_ context.Context, _ int, step *sdk.StepResult) error {
			interrupted <- step.Text
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
		OnStepCommitted: func(context.Context, int, *sdk.StepResult) error {
			close(commitStarted)
			<-ctx.Done()
			return errors.New("abort won")
		},
		OnStepInterrupted: func(_ context.Context, _ int, step *sdk.StepResult) error {
			interrupted <- step.Text
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
