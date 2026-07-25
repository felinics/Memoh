package engine

import (
	"context"
	"reflect"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	agentdomain "github.com/memohai/memoh/domains/agent"
)

type agentToolPlaceholderProvider struct{}

func (*agentToolPlaceholderProvider) Name() string { return "tool-placeholder-mock" }

func (*agentToolPlaceholderProvider) ListModels(context.Context) ([]sdk.Model, error) {
	return nil, nil
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
// produces a lightweight agentdomain.ToolCallInputStart (name + call ID, no input)
// when the SDK emits ToolInputStartPart, followed by a agentdomain.ToolCallStart
// carrying the fully-assembled Input when StreamToolCallPart arrives. The
// early input-start lets the Web UI render the tool block while arguments are
// still streaming, while IM adapters (which do not map input-start) keep their
// single-start behavior and avoid duplicate "running" messages.
func TestAgentStreamEmitsToolCallInputStartThenStart(t *testing.T) {
	t.Parallel()

	a := New(Deps{})

	var events []agentdomain.StreamEvent
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
	if events[0].Type != agentdomain.AgentStart {
		t.Fatalf("expected first event %q, got %#v", agentdomain.AgentStart, events[0])
	}
	if events[1].Type != agentdomain.ToolCallInputStart || events[1].ToolCallID != "call-1" || events[1].ToolName != "write" {
		t.Fatalf("unexpected tool call input start event: %#v", events[1])
	}
	if events[1].Input != nil {
		t.Fatalf("expected tool call input start to carry no input, got %#v", events[1].Input)
	}
	if events[2].Type != agentdomain.ToolCallStart || events[2].ToolCallID != "call-1" || events[2].ToolName != "write" {
		t.Fatalf("unexpected tool call start event: %#v", events[2])
	}
	expectedInput := map[string]any{"path": "/tmp/long.txt"}
	if !reflect.DeepEqual(events[2].Input, expectedInput) {
		t.Fatalf("expected tool call start input %#v, got %#v", expectedInput, events[2].Input)
	}
	if events[3].Type != agentdomain.AgentEnd {
		t.Fatalf("expected terminal event %q, got %#v", agentdomain.AgentEnd, events[3])
	}
}
