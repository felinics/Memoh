package serverruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/channel/inbound"
	intrpc "github.com/felinics/memoh/internal/rpc"
)

// legacyQueueCode is the decoding of a client built before the envelope: the
// runtime client kept the status message only for Unknown, and the queue call
// read the error text as a queue code.
func legacyQueueCode(err error) string {
	if status.Code(err) == codes.Unknown {
		err = errors.New(status.Convert(err).Message())
	}
	return inbound.NormalizeQueueCommandCode(err.Error())
}

// A client built before the envelope finds no queue code in the server
// encoding and falls back to its generic handling.
func TestLegacyClientLosesQueueCode(t *testing.T) {
	for _, code := range queueCodes {
		if got := legacyQueueCode(status.Error(codes.Unknown, code)); got != code {
			t.Fatalf("%s: the replica does not read the legacy encoding (%q)", code, got)
		}
		handlers := Handlers(nil, &queueHandlerStub{err: inbound.NewQueueCommandError(code)}, nil, nil)
		_, err := handlers[MethodQueueEnqueueSteer](context.Background(), json.RawMessage(`{"bot_id":"bot-1"}`))
		if reason, _ := intrpc.ReasonOf(err); reason != code {
			t.Fatalf("%s: server sent %v", code, err)
		}
		if got := legacyQueueCode(status.FromProto(status.Convert(err).Proto()).Err()); got != "" {
			t.Fatalf("%s: legacy client read %q", code, got)
		}
	}
}
