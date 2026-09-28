package serverruntime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/felinics/memoh/internal/channel/inbound"
	intrpc "github.com/felinics/memoh/internal/rpc"
)

type queueHandlerStub struct {
	steer    []inbound.QueueCommandInput
	followUp []inbound.QueueCommandInput
	err      error
}

func (s *queueHandlerStub) EnqueueSteer(_ context.Context, input inbound.QueueCommandInput) error {
	s.steer = append(s.steer, input)
	return s.err
}

func (s *queueHandlerStub) EnqueueFollowUp(_ context.Context, input inbound.QueueCommandInput) error {
	s.followUp = append(s.followUp, input)
	return s.err
}

func TestQueueRPCHandlersKeepQueueOperationsSeparate(t *testing.T) {
	stub := &queueHandlerStub{}
	handlers := Handlers(nil, stub, nil, nil)
	want := inbound.QueueCommandInput{
		BotID: "bot-1", SessionID: "session-1", InvocationID: "channel:42:queue:steer", Text: "use bun",
	}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := handlers[MethodQueueEnqueueSteer](context.Background(), payload); err != nil {
		t.Fatalf("steer handler error = %v", err)
	}
	if _, err := handlers[MethodQueueEnqueueFollowUp](context.Background(), payload); err != nil {
		t.Fatalf("follow-up handler error = %v", err)
	}
	if len(stub.steer) != 1 || len(stub.followUp) != 1 {
		t.Fatalf("calls = steer %#v, follow-up %#v", stub.steer, stub.followUp)
	}
	if stub.steer[0] != want || stub.followUp[0] != want {
		t.Fatalf("RPC changed queue input: steer %#v, follow-up %#v, want %#v", stub.steer[0], stub.followUp[0], want)
	}
}

func TestQueueRPCHandlerPublishesOnlyStableQueueCode(t *testing.T) {
	stub := &queueHandlerStub{err: inbound.NewQueueCommandError(inbound.QueueCommandCodeNoActiveRun)}
	handlers := Handlers(nil, stub, nil, nil)
	payload := json.RawMessage(`{"bot_id":"bot-1","session_id":"session-1","invocation_id":"channel:42:queue:steer","text":"use bun"}`)

	_, err := handlers[MethodQueueEnqueueSteer](context.Background(), payload)
	if reason, ok := intrpc.ReasonOf(err); !ok || reason != inbound.QueueCommandCodeNoActiveRun {
		t.Fatalf("handler error = %v, want stable queue code as the reason", err)
	}
}

func TestQueueCommandCodeAcceptsOnlyStableRPCVocabulary(t *testing.T) {
	if got := queueCommandCode(inbound.NewQueueCommandError(inbound.QueueCommandCodeConflict)); got != inbound.QueueCommandCodeConflict {
		t.Fatalf("stable code = %q", got)
	}
	if got := queueCommandCode(assertionError("database diagnostic")); got != "" {
		t.Fatalf("unsafe error became user-visible code %q", got)
	}
}

type assertionError string

func (e assertionError) Error() string { return string(e) }
