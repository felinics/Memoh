package native

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/step"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

// agentStepBoundaryToolProvider exposes one trivially executable tool so the SDK
// advances past step 0 and runs PrepareStep for the next step.
type agentStepBoundaryToolProvider struct{}

func (*agentStepBoundaryToolProvider) Tools(context.Context, agenttools.SessionContext) ([]toolexec.Tool, error) {
	return []toolexec.Tool{{
		Name:        "probe",
		Description: "probe",
		Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
			return toolexec.OutputFromValue("ok"), nil
		},
	}}, nil
}

// TestAgentStreamCheckpointWaitsForStepGoroutineToExit covers the case where the
// SDK has run a whole step ahead of the event loop through the stream's buffer.
// Only the loop's own prefix looks unfinished; the run has in fact committed
// step 0 and is preparing step 1. Checkpointing that prefix would duplicate a
// durable answer, and reading the prepared-message capture while PrepareStep is
// still writing it is a data race — so this test is meaningful under -race.
func TestAgentStreamCheckpointWaitsForStepGoroutineToExit(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	injectCh := make(chan InjectMessage, 1)
	injectCh <- InjectMessage{Text: "steering", HeaderifiedText: "steering"}

	agent := New(Deps{})
	agent.SetToolProviders([]agenttools.ToolProvider{&agentStepBoundaryToolProvider{}})
	var calls atomic.Int32
	provider := agentStreamTestProvider(func(ctx context.Context, _ sdk.Request) (<-chan sdk.StreamPart, error) {
		if calls.Add(1) == 1 {
			return closedAgentTestStream(
				&sdk.StartStepPart{},
				&sdk.TextDeltaPart{ID: "text", Text: "working"},
				&sdk.StreamToolCallPart{ToolCallID: "call-1", ToolName: "probe", Input: toolexec.ArgumentsFromValue(map[string]any{})},
				&sdk.FinishStepPart{FinishReason: sdk.FinishReasonToolCalls},
			), nil
		}
		ch := make(chan sdk.StreamPart)
		go func() { <-ctx.Done(); close(ch) }()
		return ch, nil
	})

	interrupted := make(chan *step.Record, 4)
	events := agent.Stream(ctx, RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("hi")},
		Identity:         SessionContext{BotID: "bot-1"},
		SupportsToolCall: true,
		InjectCh:         injectCh,
		OnStepCommitted:  func(context.Context, int, *step.Record) (StepDirective, error) { return StepDirective{}, nil },
		OnStepInterrupted: func(_ context.Context, _ int, step *step.Record) error {
			interrupted <- step
			return nil
		},
	})

	if first, ok := <-events; !ok || first.Type != EventAgentStart {
		t.Fatalf("first event = %#v", first)
	}
	// Deliberately not synchronized with the SDK goroutine: a signal from it
	// would order the two sides and hide exactly the race under test.
	time.Sleep(300 * time.Millisecond)
	cancel()
	for range events {
	}

	select {
	case step := <-interrupted:
		t.Fatalf("checkpointed a committed step: text=%q messages=%d", step.Result.Text, len(step.Messages))
	default:
	}
}
