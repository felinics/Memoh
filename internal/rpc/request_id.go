package rpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/felinics/memoh/internal/logger"
)

// RequestIDMetadataKey carries the caller's request id, so that what the
// callee logs for a call reports the id of the work that made it.
const RequestIDMetadataKey = "x-request-id"

func UnaryClientRequestID(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	return invoker(withOutgoingRequestID(ctx), method, req, reply, cc, opts...)
}

func StreamClientRequestID(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return streamer(withOutgoingRequestID(ctx), desc, cc, method, opts...)
}

// UnaryServerRequestID puts the caller's request id into the call context. It
// runs before the result interceptor so the result line reports it.
func UnaryServerRequestID(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	return handler(withIncomingRequestID(ctx), req)
}

func StreamServerRequestID(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx := withIncomingRequestID(stream.Context())
	return handler(srv, &contextStream{ServerStream: stream, ctx: ctx})
}

func withOutgoingRequestID(ctx context.Context) context.Context {
	id := logger.RequestIDFromContext(ctx)
	if id == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, RequestIDMetadataKey, id)
}

func withIncomingRequestID(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}
	values := md.Get(RequestIDMetadataKey)
	if len(values) == 0 {
		return ctx
	}
	return logger.ContextWithRequestID(ctx, values[0])
}

// contextStream is a server stream whose context carries values added by an
// interceptor.
type contextStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextStream) Context() context.Context { return s.ctx }
