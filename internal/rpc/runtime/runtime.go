package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/rpc"
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

// errPublic is the identity of a Public error restored by the client. Its
// text is the peer's adapter message.
var errPublic = errors.New("internal runtime call failed")

// publicReason is how a Public error travels in the envelope: its text goes
// under the adapter message key.
var publicReason = rpc.Reason{Err: errPublic, Reason: "runtime.public_error", Code: codes.Unknown, Message: "internal runtime call failed"}

// reasons is the wire table of this transport.
var reasons = rpc.Reasons{publicReason}

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
	handlers map[string]Handler
}

// NewServer serves handlers by method name. The RPC result line, written by
// the server interceptor, records each call.
func NewServer(handlers map[string]Handler) *Server {
	return &Server{handlers: handlers}
}

func (s *Server) Call(ctx context.Context, req *runtimepb.CallRequest) (*runtimepb.CallResponse, error) {
	method := strings.TrimSpace(req.GetMethod())
	handler := s.handlers[method]
	if handler == nil {
		return nil, status.Error(codes.Unimplemented, "runtime method is not implemented")
	}
	result, err := handler(ctx, json.RawMessage(req.GetPayload()))
	if err != nil {
		rpc.RecordError(ctx, fmt.Errorf("runtime method %s: %w", method, err))
		return nil, encodeError(ctx, err)
	}
	if result == nil {
		return &runtimepb.CallResponse{}, nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		rpc.RecordError(ctx, fmt.Errorf("encode runtime method %s result: %w", method, err))
		return nil, status.Error(codes.Internal, "internal runtime result encoding failed")
	}
	return &runtimepb.CallResponse{Payload: data}, nil
}

// encodeError maps a handler error to the status the client receives. A
// status built by the handler group travels as it is. A Public error without
// a catalog code travels as its adapter message. Anything else is answered by
// rpc.AnswerStatus: a catalog apperror as its code and args, also when a
// Public error wraps it, and any other error as the generic code for its
// fault.
func encodeError(ctx context.Context, err error) error {
	if _, direct := err.(grpcStatusError); direct { //nolint:errorlint // deliberate direct assertion: only a status built by this layer is wire vocabulary; a wrapped one is a downstream leak
		return err
	}
	var public *publicError
	if _, catalog := apperror.Lookup(apperror.CodeOf(err)); !catalog && errors.As(err, &public) {
		return publicReason.Status(public.Error())
	}
	return rpc.AnswerStatus(ctx, err)
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
		return decodeError(err)
	}
	if output == nil || len(resp.GetPayload()) == 0 {
		return nil
	}
	return json.Unmarshal(resp.GetPayload(), output)
}

// restoredByCode restores the transport failures a status without a known
// reason reports. DeadlineExceeded and Canceled are not unavailability; they
// stay statuses for the caller to judge against its own context.
var restoredByCode = map[codes.Code]error{
	codes.Unavailable:     ErrUnavailable,
	codes.Unauthenticated: errors.Join(ErrUnavailable, ErrUnauthenticated),
}

// decodeError maps a received status to what the caller sees, by rpc.Decode.
// A status this transport does not restore stays readable on the result, for
// the handler group's client to restore its own reasons.
func decodeError(err error) error {
	return rpc.Decode(err, reasons, restoredByCode)
}
