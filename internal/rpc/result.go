package rpc

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/errs"
)

// healthServicePrefix names the gRPC health service. A passing health check
// is not recorded.
var healthServicePrefix = "/" + grpc_health_v1.Health_ServiceDesc.ServiceName + "/"

type resultRecordKey struct{}

// resultRecord holds the cause a handler hands to the result line before it
// maps the error to a status.
type resultRecord struct {
	cause atomic.Pointer[error]
}

// RecordError hands err to the RPC result line. A handler calls it when it
// maps a failure to a status that no longer carries the cause, so that the
// record still attributes the original error. Outside an RPC it does nothing.
func RecordError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	if record, ok := ctx.Value(resultRecordKey{}).(*resultRecord); ok {
		record.cause.Store(&err)
	}
}

// UnaryServerResult writes the one result line of each unary call and returns
// the status with the fault that line attributes, as attributed describes. A
// handler panic is recorded and then propagated unchanged.
func UnaryServerResult(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (reply any, err error) {
		record := &resultRecord{}
		ctx = context.WithValue(ctx, resultRecordKey{}, record)
		start := time.Now()
		defer func() {
			if value := recover(); value != nil {
				logPanic(ctx, logger, info.FullMethod, value, start)
				panic(value)
			}
			err = logResult(ctx, logger, info.FullMethod, record, err, start)
		}()
		return handler(ctx, req)
	}
}

// StreamServerResult is the streaming counterpart of UnaryServerResult. One
// stream is one unit of work and gets one result line.
func StreamServerResult(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		record := &resultRecord{}
		ctx := context.WithValue(stream.Context(), resultRecordKey{}, record)
		start := time.Now()
		defer func() {
			if value := recover(); value != nil {
				logPanic(ctx, logger, info.FullMethod, value, start)
				panic(value)
			}
			err = logResult(ctx, logger, info.FullMethod, record, err, start)
		}()
		return handler(srv, &resultStream{ServerStream: stream, ctx: ctx})
	}
}

// logResult writes the result line of a call that returned returned, and
// returns the error the client receives.
func logResult(ctx context.Context, logger *slog.Logger, method string, record *resultRecord, returned error, start time.Time) error {
	if returned == nil && strings.HasPrefix(method, healthServicePrefix) {
		return nil
	}
	cause := returned
	if returned != nil {
		if recorded := record.cause.Load(); recorded != nil {
			cause = *recorded
		}
	}
	writeResult(ctx, logger, method, cause, grpcCode(returned), start)
	return attributed(returned, cause)
}

// attributed returns the status the client receives for returned, carrying
// under MetadataFault the fault this server attributes cause to. A status
// that already carries a fault, such as a catalog envelope, is returned
// unchanged. Every status an internal RPC server sends thus says whose
// failure it is, and the client attributes it from that.
func attributed(returned, cause error) error {
	if returned == nil {
		return nil
	}
	st, ok := status.FromError(returned)
	if !ok {
		st = status.FromContextError(returned)
	}
	proto := st.Proto()
	for i, detail := range proto.GetDetails() {
		info := &errdetails.ErrorInfo{}
		if !detail.MessageIs(info) {
			continue
		}
		if detail.UnmarshalTo(info) != nil {
			return returned
		}
		if _, ok := info.GetMetadata()[MetadataFault]; ok {
			return returned
		}
		if info.Metadata == nil {
			info.Metadata = map[string]string{}
		}
		info.Metadata[MetadataFault] = string(errs.FaultOf(cause))
		packed, err := anypb.New(info)
		if err != nil {
			return returned
		}
		proto.Details[i] = packed
		return status.FromProto(proto).Err()
	}
	packed, err := anypb.New(&errdetails.ErrorInfo{
		Domain:   errorDomain,
		Metadata: map[string]string{MetadataFault: string(errs.FaultOf(cause))},
	})
	if err != nil {
		return returned
	}
	proto.Details = append(proto.Details, packed)
	return status.FromProto(proto).Err()
}

// logPanic records a handler panic. The server does not recover handler
// panics, so the record carries no code the client would see.
func logPanic(ctx context.Context, logger *slog.Logger, method string, value any, start time.Time) {
	writeResult(ctx, logger, method, errs.Recovered(value), codes.Unknown, start)
}

func writeResult(ctx context.Context, logger *slog.Logger, method string, cause error, code codes.Code, start time.Time) {
	result := errlog.Finish(ctx, method, cause, errlog.Options{})
	attrs := append([]slog.Attr{
		slog.String("operation", method),
		slog.String("grpc_code", code.String()),
		slog.Duration("latency", time.Since(start)),
	}, result.Attrs()...)
	logger.LogAttrs(ctx, result.Level, "rpc request", attrs...)
}

// grpcCode is the code the client receives. The server converts a returned
// context error to Canceled or DeadlineExceeded.
func grpcCode(err error) codes.Code {
	if st, ok := status.FromError(err); ok {
		return st.Code()
	}
	return status.FromContextError(err).Code()
}

// resultStream carries the result record in the stream context.
type resultStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *resultStream) Context() context.Context { return s.ctx }
