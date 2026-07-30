package native

import (
	"context"
	"reflect"
	"testing"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"
)

type agentToolPlaceholderProvider struct{}

func (*agentToolPlaceholderProvider) Name() string { return "tool-placeholder-mock" }

func (*agentToolPlaceholderProvider) ListModels(context.Context) ([]sdk.Model, error) {
	return nil, nil
}

type agentNonClosingStreamProvider struct{}

func (*agentNonClosingStreamProvider) Name() string { return "non-closing-stream-mock" }

func (*agentNonClosingStreamProvider) ListModels(context.Context) ([]sdk.Model, error) {
	return nil, nil
}

func (*agentNonClosingStreamProvider) Test(context.Context) *sdk.ProviderTestResult {
	return &sdk.ProviderTestResult{Status: sdk.ProviderStatusOK, Message: "ok"}
}

func (*agentNonClosingStreamProvider) TestModel(context.Context, string) (*sdk.ModelTestResult, error) {
	return &sdk.ModelTestResult{Supported: true, Message: "supported"}, nil
}

func (*agentNonClosingStreamProvider) DoGenerate(context.Context, sdk.GenerateParams) (*sdk.GenerateResult, error) {
	return &sdk.GenerateResult{FinishReason: sdk.FinishReasonStop}, nil
}

func (*agentNonClosingStreamProvider) DoStream(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
	return &sdk.StreamResult{Stream: make(chan sdk.StreamPart)}, nil
}

func (*agentToolPlaceholderProvider) Test(context.Context) *sdk.ProviderTestResult {
	return &sdk.ProviderTestResult{Status: sdk.ProviderStatusOK, Message: "ok"}
}

func (*agentToolPlaceholderProvider) TestModel(context.Context, string) (*sdk.ModelTestResult, error) {
	return &sdk.ModelTestResult{Supported: true, Message: "supported"}, nil
}

func (*agentToolPlaceholderProvider) DoGenerate(context.Context, sdk.GenerateParams) (*sdk.GenerateResult, error) {
	return &sdk.GenerateResult{FinishReason: sdk.FinishReasonStop}, nil
}

func (*agentToolPlaceholderProvider) DoStream(_ context.Context, _ sdk.GenerateParams) (*sdk.StreamResult, error) {
	ch := make(chan sdk.StreamPart, 8)
	go func() {
		defer close(ch)
		ch <- &sdk.StartPart{}
		ch <- &sdk.StartStepPart{}
		ch <- &sdk.ToolInputStartPart{ID: "call-1", ToolName: "write"}
		ch <- &sdk.StreamToolCallPart{
			ToolCallID: "call-1",
			ToolName:   "write",
			Input:      map[string]any{"path": "/tmp/long.txt"},
		}
		ch <- &sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}
		ch <- &sdk.FinishPart{FinishReason: sdk.FinishReasonStop}
	}()
	return &sdk.StreamResult{Stream: ch}, nil
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

	var events []StreamEvent
	for event := range a.Stream(context.Background(), RunConfig{
		Model: &sdk.Model{
			ID:       "mock-model",
			Provider: &agentToolPlaceholderProvider{},
		},
		Messages:         []sdk.Message{sdk.UserMessage("write a long file")},
		SupportsToolCall: false,
		Identity:         SessionContext{BotID: "bot-1"},
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
}

func TestAgentStreamCancellationDoesNotWaitForProviderToClose(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	events := New(Deps{}).Stream(ctx, RunConfig{
		Model: &sdk.Model{
			ID:       "mock-model",
			Provider: &agentNonClosingStreamProvider{},
		},
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
