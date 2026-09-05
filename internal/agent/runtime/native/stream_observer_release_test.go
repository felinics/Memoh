package native

import (
	"context"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
)

func TestAgentStreamObserverReleasesWhenConsumerLeaves(t *testing.T) {
	restore := streamTerminalDeliveryGrace
	streamTerminalDeliveryGrace = 50 * time.Millisecond
	t.Cleanup(func() { streamTerminalDeliveryGrace = restore })

	release := make(chan struct{})
	provider := agentStreamTestProvider(func(ctx context.Context, _ sdk.GenerateParams) (*sdk.StreamResult, error) {
		ch := make(chan sdk.StreamPart)
		send := func(part sdk.StreamPart) bool {
			select {
			case ch <- part:
				return true
			case <-ctx.Done():
				return false
			}
		}
		go func() {
			defer close(ch)
			if !send(&sdk.StartStepPart{}) || !send(&sdk.TextDeltaPart{ID: "text", Text: "first"}) {
				return
			}
			<-release
			if send(&sdk.TextDeltaPart{ID: "text", Text: "second"}) {
				send(&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop})
			}
		}()
		return &sdk.StreamResult{Stream: ch}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	events := New(Deps{}).Stream(ctx, RunConfig{
		Model:                &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:             []sdk.Message{sdk.UserMessage("task")},
		Identity:             SessionContext{BotID: "bot-1"},
		OnAgentEventObserved: func(StreamEvent) {},
	})
	for event := range events {
		if event.Type == EventTextDelta {
			break
		}
	}
	// The consumer leaves without draining: the documented unwind is to
	// cancel the context, after which every goroutine behind the stream must
	// finish on its own.
	cancel()
	close(release)

	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("observed stream did not close after the consumer left")
		}
	}
}

func TestAgentStreamObserverKeepsTheTerminalForAStalledConsumer(t *testing.T) {
	restore := streamTerminalDeliveryGrace
	streamTerminalDeliveryGrace = 50 * time.Millisecond
	t.Cleanup(func() { streamTerminalDeliveryGrace = restore })

	provider := agentStreamTestProvider(func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
		return closedAgentTestStream(
			&sdk.StartStepPart{},
			&sdk.TextDeltaPart{ID: "text", Text: "hello"},
			&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop, Usage: sdk.Usage{InputTokens: 3, OutputTokens: 1}},
		), nil
	})
	var observed []StreamEventType
	events := New(Deps{}).Stream(context.Background(), RunConfig{
		Model:                &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:             []sdk.Message{sdk.UserMessage("task")},
		Identity:             SessionContext{BotID: "bot-1"},
		OnAgentEventObserved: func(ev StreamEvent) { observed = append(observed, ev.Type) },
	})
	var got []StreamEventType
	for ev := range events {
		got = append(got, ev.Type)
		if ev.Type == EventTextDelta {
			// A consumer slower than the terminal window while the relay
			// already holds the next event: without the relay the run would
			// wait on this event itself and the terminal would get a fresh
			// window afterwards.
			time.Sleep(2 * streamTerminalDeliveryGrace)
		}
	}
	if len(got) == 0 || got[len(got)-1] != EventAgentEnd {
		t.Fatalf("consumer events = %v, want the terminal event last", got)
	}
	if len(observed) == 0 || observed[len(observed)-1] != EventAgentEnd {
		t.Fatalf("observed events = %v, want the terminal event last", observed)
	}
}
