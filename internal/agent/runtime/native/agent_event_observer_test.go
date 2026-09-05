package native

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func TestAgentStreamObservesPublicEventsThroughRunConfig(t *testing.T) {
	t.Parallel()

	var observed []StreamEventType
	var received []StreamEventType
	for event := range New(Deps{}).Stream(context.Background(), RunConfig{
		Model:    &sdk.Model{ID: "mock-model", Provider: finishedTextTestProvider("done")},
		Messages: []sdk.Message{sdk.UserMessage("task")},
		Identity: SessionContext{BotID: "bot-1"},
		OnAgentEventObserved: func(event StreamEvent) {
			observed = append(observed, event.Type)
		},
	}) {
		received = append(received, event.Type)
	}

	if len(received) == 0 || received[len(received)-1] != EventAgentEnd {
		t.Fatalf("received = %v", received)
	}
	if len(observed) != len(received) {
		t.Fatalf("observed %v, received %v", observed, received)
	}
	for i := range received {
		if observed[i] != received[i] {
			t.Fatalf("observed %v, received %v", observed, received)
		}
	}
}
