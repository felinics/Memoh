package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/rpc/runtimepb"
)

var (
	ErrUnavailable = errors.New("internal runtime unavailable")
	// ErrUnauthenticated marks a shared-secret mismatch between the server
	// and channel processes. It always accompanies ErrUnavailable so
	// availability mapping keeps working, but lets diagnostics distinguish
	// a misconfigured secret from a transient outage.
	ErrUnauthenticated = errors.New("internal runtime authentication failed")
)

type Handler func(context.Context, json.RawMessage) (any, error)

// publicError marks an error whose message is safe and meaningful to
// transport verbatim to the peer process — e.g. a platform adapter failure
// the operator must see ("telegram: chat not found"). Anything not marked
// is sanitized to an opaque internal error.
type publicError struct{ err error }

// Public wraps err for verbatim transport across the internal RPC.
// Public(nil) is nil so handlers can wrap unconditionally.
func Public(err error) error {
	if err == nil {
		return nil
	}
	return &publicError{err: err}
}

func (e *publicError) Error() string { return e.err.Error() }
func (e *publicError) Unwrap() error { return e.err }

// grpcStatusError is implemented by errors constructed via status.Error.
// Checked with a direct type assertion (no unwrap): only a status built by
// this layer's handlers is intentional wire vocabulary. A status buried in
// a wrap chain (e.g. a workspace-bridge Unavailable from a stopped bot
// container) is a downstream detail that must NOT leak — the peer would
// misdiagnose it as a server↔channel link failure.
type grpcStatusError interface {
	GRPCStatus() *status.Status
	error
}

type Server struct {
	runtimepb.UnimplementedRuntimeServiceServer
	logger   *slog.Logger
	handlers map[string]Handler
}

func NewServer(log *slog.Logger, handlers map[string]Handler) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{logger: log.With(slog.String("component", "runtime_rpc")), handlers: handlers}
}

func (s *Server) Call(ctx context.Context, req *runtimepb.CallRequest) (*runtimepb.CallResponse, error) {
	method := strings.TrimSpace(req.GetMethod())
	handler := s.handlers[method]
	if handler == nil {
		return nil, status.Error(codes.Unimplemented, "runtime method is not implemented")
	}
	result, err := handler(ctx, json.RawMessage(req.GetPayload()))
	if err != nil {
		s.logger.Error("runtime rpc call failed", slog.String("method", method), slog.Any("error", err))
		var public *publicError
		if errors.As(err, &public) {
			return nil, status.Error(codes.Unknown, public.Error())
		}
		if _, direct := err.(grpcStatusError); direct { //nolint:errorlint // deliberate direct assertion: only a status built by this layer is wire vocabulary; a wrapped one is a downstream leak
			return nil, err
		}
		return nil, status.Error(codes.Internal, "internal runtime operation failed")
	}
	if result == nil {
		return &runtimepb.CallResponse{}, nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		s.logger.Error("runtime rpc result encoding failed", slog.String("method", method), slog.Any("error", err))
		return nil, status.Error(codes.Internal, "internal runtime result encoding failed")
	}
	return &runtimepb.CallResponse{Payload: data}, nil
}

type Client struct {
	client runtimepb.RuntimeServiceClient
}

func NewClient(conn grpc.ClientConnInterface) *Client {
	return &Client{client: runtimepb.NewRuntimeServiceClient(conn)}
}

func (c *Client) Call(ctx context.Context, method string, input, output any) error {
	var payload []byte
	var err error
	if input != nil {
		payload, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	resp, err := c.client.Call(ctx, &runtimepb.CallRequest{Method: method, Payload: payload})
	if err != nil {
		switch status.Code(err) {
		case codes.Unavailable, codes.DeadlineExceeded:
			return errors.Join(ErrUnavailable, err)
		case codes.Unauthenticated:
			return errors.Join(ErrUnavailable, ErrUnauthenticated, err)
		case codes.Unknown:
			// Unknown carries a Public() message from the peer's handler;
			// strip the rpc-status envelope so callers (and users) see the
			// original adapter error text.
			return errors.New(status.Convert(err).Message())
		default:
			return err
		}
	}
	if output == nil || len(resp.GetPayload()) == 0 {
		return nil
	}
	return json.Unmarshal(resp.GetPayload(), output)
}
