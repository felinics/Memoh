package serverruntime

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/channel/inbound"
	"github.com/felinics/memoh/internal/errs"
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

// A queue refusal crosses the RPC as its catalog code. The client forwards it:
// a refusal stays the end user's, a server's unavailability is a dependency's,
// and the received status stays on the chain.
func TestQueueCodesSurviveEnvelope(t *testing.T) {
	for _, code := range queueCodes {
		t.Run(code, func(t *testing.T) {
			handlers := Handlers(nil, &queueHandlerStub{err: inbound.NewQueueCommandError(code)}, nil, nil)
			err := newQueueClient(t, handlers).EnqueueSteer(context.Background(), inbound.QueueCommandInput{BotID: "bot-1"})
			if got := inbound.QueueCommandErrorCode(err); got != code {
				t.Fatalf("queue code = %q from %v, want %q", got, err, code)
			}
			want := apperror.FaultClient
			if definition, _ := apperror.Lookup(apperror.Code(code)); definition.HTTPStatus >= 500 {
				want = apperror.FaultDependency
			}
			if _, fault := errs.Answer(context.Background(), err); fault != want {
				t.Fatalf("fault = %s, want %s", fault, want)
			}
			if rpc.Received(err).Error() == err.Error() {
				t.Fatal("received status is not on the chain")
			}
		})
	}
}

// Servers sent a queue code as Unknown with the code as the status message
// before the envelope. Without the envelope the queue call returns the status
// as received, without a queue code.
func TestQueueCallIgnoresPreEnvelopeText(t *testing.T) {
	for _, code := range queueCodes {
		sent := status.Error(codes.Unknown, code)
		err := newQueueClient(t, map[string]runtimeRpc.Handler{
			MethodQueueEnqueueSteer: func(context.Context, json.RawMessage) (any, error) { return nil, sent },
		}).EnqueueSteer(context.Background(), inbound.QueueCommandInput{BotID: "bot-1"})
		if got := inbound.QueueCommandErrorCode(err); got != "" {
			t.Fatalf("%s: queue code = %q from a status without the envelope", code, got)
		}
		if status.Code(err) != codes.Unknown || status.Convert(err).Message() != code {
			t.Fatalf("%s: got %v, want the received status", code, err)
		}
	}
}

// A reason outside the queue vocabulary is not read as a queue code, even
// when the status message is one.
func TestQueueCallIgnoresOtherReasons(t *testing.T) {
	other := rpc.Reason{Reason: "test.other", Code: codes.Unknown, Message: inbound.QueueCommandCodeConflict}.Status("")
	err := newQueueClient(t, map[string]runtimeRpc.Handler{
		MethodQueueEnqueueSteer: func(context.Context, json.RawMessage) (any, error) { return nil, other },
	}).EnqueueSteer(context.Background(), inbound.QueueCommandInput{BotID: "bot-1"})
	if code := inbound.QueueCommandErrorCode(err); code != "" {
		t.Fatalf("queue code = %q from a foreign reason", code)
	}
	if reason, ok := rpc.ReasonOf(err); !ok || reason != "test.other" {
		t.Fatalf("got %v, want the received status with its reason", err)
	}
}
