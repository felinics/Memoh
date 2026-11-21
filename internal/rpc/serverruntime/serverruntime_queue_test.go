package serverruntime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/felinics/memoh/internal/channel/inbound"
)

type queueHandlerStub struct {
	steer []inbound.QueueCommandInput
	err   error
}

func (s *queueHandlerStub) EnqueueSteer(_ context.Context, input inbound.QueueCommandInput) error {
	s.steer = append(s.steer, input)
	return s.err
}

func TestQueueRPCHandlerPassesSteerThrough(t *testing.T) {
	stub := &queueHandlerStub{}
	want := inbound.QueueCommandInput{
		BotID: "bot-1", SessionID: "session-1", InvocationID: "channel:42:queue:steer", Text: "use bun",
	}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Handlers(nil, stub, nil, nil)[MethodQueueEnqueueSteer](context.Background(), payload); err != nil {
		t.Fatalf("steer handler error = %v", err)
	}
	if len(stub.steer) != 1 || stub.steer[0] != want {
		t.Fatalf("RPC changed steer input: %#v, want %#v", stub.steer, want)
	}
}
