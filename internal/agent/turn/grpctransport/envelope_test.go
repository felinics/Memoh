package grpctransport

import (
	"context"
	"errors"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/agent/turn/turnpb"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/rpc"
)

// statusServer answers every unary call with err, as a peer that writes a
// given encoding would.
type statusServer struct {
	turnpb.UnimplementedTurnServiceServer
	err error
}

func (s *statusServer) StopTurn(context.Context, *turnpb.JsonRequest) (*turnpb.JsonResponse, error) {
	return nil, s.err
}

func (s *statusServer) RuntimeCommands(context.Context, *turnpb.JsonRequest) (*turnpb.JsonResponse, error) {
	return nil, s.err
}

func newStatusClient(t *testing.T, err error) *Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	turnpb.RegisterTurnServiceServer(server, &statusServer{err: err})
	go func() { _ = server.Serve(lis) }()
	conn, dialErr := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if dialErr != nil {
		t.Fatalf("dial: %v", dialErr)
	}
	t.Cleanup(func() { _ = conn.Close(); server.Stop(); _ = lis.Close() })
	return NewClient(conn)
}

func TestTurnSentinelsSurviveEnvelope(t *testing.T) {
	for _, entry := range turnReasons {
		encodings := map[string]error{
			"envelope": entry.Status(""),
			"server":   (*Server)(nil).mapError(context.Background(), "test", entry.Err),
		}
		for name, wire := range encodings {
			t.Run(entry.Reason+"/"+name, func(t *testing.T) {
				_, err := newStatusClient(t, wire).StopTurn(context.Background(), turn.StopCommand{TeamID: "team-1"})
				for _, other := range turnReasons {
					if got, want := errors.Is(err, other.Err), errors.Is(other.Err, entry.Err); got != want {
						t.Fatalf("errors.Is(%v, %v) = %v", err, other.Err, got)
					}
				}
				received := rpc.Received(err)
				if status.Code(received) != entry.Code || status.Convert(received).Message() != entry.Message {
					t.Fatalf("received status = %v", received)
				}
				if !errs.Analyze(context.Background(), err).Remote {
					t.Fatal("restored sentinel is not marked remote")
				}
			})
		}
	}
}

func TestTurnCatalogCodeSurvivesEnvelope(t *testing.T) {
	sent := apperror.New(apperror.CodeBotNameTaken, map[string]string{"field": "name"})
	wire := rpc.AnswerStatus(context.Background(), sent)
	if wire == nil {
		t.Fatal("catalog apperror not encoded")
	}
	client := newStatusClient(t, wire)
	_, stopErr := client.StopTurn(context.Background(), turn.StopCommand{TeamID: "team-1"})
	_, controlErr := client.RuntimeCommands(context.Background(), turn.RuntimeControlRequest{TeamID: "team-1"})
	for name, err := range map[string]error{"stop": stopErr, "runtime control": controlErr} {
		if apperror.CodeOf(err) != apperror.CodeBotNameTaken || apperror.ArgsOf(err)["field"] != "name" {
			t.Fatalf("%s: got %v (args %v), want the catalog error", name, err, apperror.ArgsOf(err))
		}
		if _, ok := rpc.ReasonOf(rpc.Received(err)); !ok {
			t.Fatalf("%s: received status is not on the chain", name)
		}
	}
}

// A status without the envelope is returned as received: the status codes,
// the "turn deferred" message and the runtime control and External Agent
// feedback prefixes that servers sent before the envelope restore nothing.
func TestPreEnvelopeEncodingIsNotRestored(t *testing.T) {
	encodings := map[string]error{
		"runtime control prefix": status.Error(codes.FailedPrecondition, "memoh-runtime-control:"+string(apperror.CodeBotNameTaken)),
		"feedback prefix":        status.Error(codes.FailedPrecondition, `memoh-acp-feedback:{"code":"agent_dependency_missing","args":{"dep_id":"codex"}}`),
	}
	for _, entry := range turnReasons {
		encodings[entry.Reason+" status code"] = status.Error(entry.Code, entry.Message)
	}
	for name, wire := range encodings {
		t.Run(name, func(t *testing.T) {
			client := newStatusClient(t, wire)
			_, stopErr := client.StopTurn(context.Background(), turn.StopCommand{TeamID: "team-1"})
			_, controlErr := client.RuntimeCommands(context.Background(), turn.RuntimeControlRequest{TeamID: "team-1"})
			for path, err := range map[string]error{"stop": stopErr, "runtime control": controlErr} {
				for _, entry := range turnReasons {
					if errors.Is(err, entry.Err) {
						t.Fatalf("%s: restored %v", path, entry.Err)
					}
				}
				if code := apperror.CodeOf(err); code != "" {
					t.Fatalf("%s: restored the catalog code %q", path, code)
				}
				if status.Code(err) != status.Code(wire) || status.Convert(err).Message() != status.Convert(wire).Message() {
					t.Fatalf("%s: got %v, want the received status %v", path, err, wire)
				}
			}
		})
	}
}

// The envelope's reason identifies the sentinel; the status code does not.
func TestTurnEnvelopeReasonWinsOverCode(t *testing.T) {
	for _, entry := range turnReasons {
		wire := rpc.Reason{Reason: entry.Reason, Code: codes.Unknown, Message: entry.Message}.Status("")
		if err := mapClientError(wire); !errors.Is(err, entry.Err) {
			t.Fatalf("%s: got %v, want %v", entry.Reason, err, entry.Err)
		}
	}
}
