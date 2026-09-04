package native

import (
	"context"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
)

func TestAgentStreamObserverReleasesWhenConsumerLeaves(t *testing.T) {
	restore := streamObserverDeliveryGrace
	streamObserverDeliveryGrace = 50 * time.Millisecond
	t.Cleanup(func() { streamObserverDeliveryGrace = restore })

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
