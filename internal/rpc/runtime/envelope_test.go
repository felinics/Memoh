package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/rpc"
	"github.com/felinics/memoh/internal/rpc/runtimepb"
)

// callOverWire runs handlerErr through a real server and client.
func callOverWire(t *testing.T, handlerErr error) error {
	t.Helper()
	return NewClient(dialOverWire(t, handlerErr)).Call(context.Background(), "m", nil, nil)
}

// dialOverWire serves handlerErr from method "m" of a real server.
func dialOverWire(t *testing.T, handlerErr error) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	runtimepb.RegisterRuntimeServiceServer(server, NewServer(map[string]Handler{
		"m": func(context.Context, json.RawMessage) (any, error) { return nil, handlerErr },
	}))
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
	return conn
}

func TestPublicErrorSurvivesEnvelope(t *testing.T) {
	const text = "telegram: chat not found"
	for name, handlerErr := range map[string]error{
		"envelope": publicReason.Status(text),
		"server":   Public(errors.New(text)),
	} {
		t.Run(name, func(t *testing.T) {
			err := callOverWire(t, handlerErr)
			if err.Error() != text {
				t.Fatalf("text = %q, want the adapter message", err.Error())
			}
			if !errors.Is(err, errPublic) {
				t.Fatalf("%v lost the public identity", err)
			}
			if status.Code(rpc.Received(err)) != codes.Unknown {
				t.Fatalf("received status = %v", rpc.Received(err))
			}
			if !errs.Analyze(context.Background(), err).Remote {
				t.Fatal("restored error is not marked remote")
			}
		})
	}
}

// Servers sent a Public error as Unknown with the adapter text as the status
// message before the envelope. Without the envelope the status is returned as
// received, not as a Public error.
func TestPreEnvelopePublicErrorIsNotRestored(t *testing.T) {
	const text = "telegram: chat not found"
	err := callOverWire(t, status.Error(codes.Unknown, text))
	if errors.Is(err, errPublic) || err.Error() == text {
		t.Fatalf("%v was restored as a Public error", err)
	}
	if status.Code(err) != codes.Unknown || status.Convert(err).Message() != text {
		t.Fatalf("got %v, want the received status", err)
	}
}

func TestCatalogCodeSurvivesEnvelope(t *testing.T) {
	wire := rpc.AnswerStatus(context.Background(), apperror.New(apperror.CodeBotNameTaken, map[string]string{"field": "name"}))
	if wire == nil {
		t.Fatal("catalog apperror not encoded")
	}
	err := callOverWire(t, wire)
	if apperror.CodeOf(err) != apperror.CodeBotNameTaken || apperror.ArgsOf(err)["field"] != "name" {
		t.Fatalf("got %v (args %v), want the catalog error", err, apperror.ArgsOf(err))
	}
	if reason, ok := rpc.ReasonOf(rpc.Received(err)); !ok || reason != string(apperror.CodeBotNameTaken) {
		t.Fatalf("received reason = %q, %v", reason, ok)
	}
}

// A status the transport does not recognize is left for the handler group's
// client, with its ErrorInfo intact.
func TestUnrecognizedEnvelopePassesThrough(t *testing.T) {
	sent := rpc.Reason{Reason: "test.unregistered_reason", Code: codes.NotFound, Message: "unregistered reason"}.Status("")
	err := callOverWire(t, sent)
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v", status.Code(err))
	}
	if reason, ok := rpc.ReasonOf(err); !ok || reason != "test.unregistered_reason" {
		t.Fatalf("reason = %q, %v", reason, ok)
	}
}

func TestUnavailableJoinsSentinel(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.Unauthenticated} {
		err := decodeError(status.Error(code, "x"))
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%v: %v is not ErrUnavailable", code, err)
		}
	}
}

func TestDeadlineAndCancelAreNotUnavailable(t *testing.T) {
	for _, code := range []codes.Code{codes.DeadlineExceeded, codes.Canceled} {
		sent := status.Error(code, "x")
		err := decodeError(sent)
		if errors.Is(err, ErrUnavailable) {
			t.Fatalf("%v became ErrUnavailable", code)
		}
		if status.Code(err) != code {
			t.Fatalf("%v: code = %v", code, status.Code(err))
		}
	}
}
