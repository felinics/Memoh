package serverruntime

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/channel/inbound"
	"github.com/felinics/memoh/internal/rpc"
	runtimeRpc "github.com/felinics/memoh/internal/rpc/runtime"
	"github.com/felinics/memoh/internal/rpc/runtimepb"
)

var queueCodes = []string{
	inbound.QueueCommandCodeNoActiveRun,
	inbound.QueueCommandCodeOverloaded,
	inbound.QueueCommandCodeUnavailable,
	inbound.QueueCommandCodeConflict,
	inbound.QueueCommandCodeInvalid,
	inbound.QueueCommandCodeUnsupported,
	inbound.QueueCommandCodeCapacity,
	inbound.QueueCommandCodeFollowUpUnsupportedChannel,
}

// newQueueClient serves the queue methods with handlers over a real
// runtime server and client.
func newQueueClient(t *testing.T, handlers map[string]runtimeRpc.Handler) *Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	runtimepb.RegisterRuntimeServiceServer(server, runtimeRpc.NewServer(handlers))
	go func() { _ = server.Serve(lis) }()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(); server.Stop(); _ = lis.Close() })
	return NewClient(runtimeRpc.NewClient(conn))
}

func TestQueueCodesSurviveBothEncodings(t *testing.T) {
	for _, code := range queueCodes {
		envelope := func(context.Context, json.RawMessage) (any, error) { return nil, queueStatus(code) }
		encodings := map[string]map[string]runtimeRpc.Handler{
			"envelope": {MethodQueueEnqueueSteer: envelope},
			"legacy":   Handlers(nil, &queueHandlerStub{err: inbound.NewQueueCommandError(code)}, nil, nil),
		}
		for name, handlers := range encodings {
			t.Run(code+"/"+name, func(t *testing.T) {
				err := newQueueClient(t, handlers).EnqueueSteer(context.Background(), inbound.QueueCommandInput{BotID: "bot-1"})
				if got := inbound.QueueCommandErrorCode(err); got != code {
					t.Fatalf("queue code = %q from %v, want %q", got, err, code)
				}
				if rpc.Received(err).Error() == err.Error() {
					t.Fatal("received status is not on the chain")
				}
			})
		}
	}
}

// A reason outside the queue vocabulary is not read as a queue code, even
// when the legacy text would match one.
func TestQueueCallIgnoresOtherReasons(t *testing.T) {
	// Unknown makes the runtime client restore the message as the error text,
	// which is what the legacy matching reads.
	other := rpc.Reason{Reason: "test.other", Code: codes.Unknown, Message: inbound.QueueCommandCodeConflict}.Status("")
	err := newQueueClient(t, map[string]runtimeRpc.Handler{
		MethodQueueEnqueueSteer: func(context.Context, json.RawMessage) (any, error) { return nil, other },
	}).EnqueueSteer(context.Background(), inbound.QueueCommandInput{BotID: "bot-1"})
	if err == nil || err.Error() != inbound.QueueCommandCodeConflict {
		t.Fatalf("err = %v, want the status message as text", err)
	}
	if code := inbound.QueueCommandErrorCode(err); code != "" {
		t.Fatalf("queue code = %q from a foreign reason", code)
	}
}
