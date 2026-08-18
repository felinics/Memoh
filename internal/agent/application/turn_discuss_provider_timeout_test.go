package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/runtime/native"
)

type providerTimeoutDiscussAgentStreamer struct{}

func (*providerTimeoutDiscussAgentStreamer) Stream(context.Context, native.RunConfig) <-chan native.StreamEvent {
	ch := make(chan native.StreamEvent, 2)
	ch <- native.StreamEvent{
		Type:  native.EventError,
		Error: `Get "https://provider.invalid": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`,
	}
	ch <- native.StreamEvent{
		Type:     native.EventAgentAbort,
		Messages: json.RawMessage(`[]`),
	}
	close(ch)
	return ch
}

func TestDiscussProviderTimeoutPersistsInterruptedTurnMarker(t *testing.T) {
	resolver := &fakeDiscussService{resolveResult: ResolveRunConfigResult{ModelID: "model-1"}}
	service := newDiscussTestService(&fakeRunner{}, &providerTimeoutDiscussAgentStreamer{}, resolver)
	// Keep the application watchdog well beyond the synthetic provider failure:
	// this test specifically proves the marker does not depend on DidFire().
	service.streamIdleTimeout = time.Second

	h, err := service.StartTurn(context.Background(), discussCommand())
	if err != nil {
		t.Fatal(err)
	}
	drainDiscuss(t, h)

	if resolver.storeCalls != 1 {
		t.Fatalf("store calls = %d, want one interrupted turn", resolver.storeCalls)
	}
	if len(resolver.storedMessages) != 1 {
		t.Fatalf("stored messages = %#v, want one interrupted marker", resolver.storedMessages)
	}
	message := resolver.storedMessages[0]
	if message.Role != sdk.MessageRoleAssistant {
		t.Fatalf("stored role = %q, want assistant", message.Role)
	}
	if len(message.Content) != 1 {
		t.Fatalf("stored content = %#v, want one text part", message.Content)
	}
	text, ok := message.Content[0].(sdk.TextPart)
	if !ok || text.Text != interruptedTurnMarker {
		t.Fatalf("stored content = %#v, want %q", message.Content, interruptedTurnMarker)
	}
}
