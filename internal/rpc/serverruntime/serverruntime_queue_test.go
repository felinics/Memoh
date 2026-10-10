package serverruntime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/felinics/memoh/internal/channel/inbound"
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
