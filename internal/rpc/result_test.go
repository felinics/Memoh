package rpc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/logger"
	"github.com/felinics/memoh/internal/rpc"
	rpcruntime "github.com/felinics/memoh/internal/rpc/runtime"
	"github.com/felinics/memoh/internal/rpc/runtimepb"
)

const resultTestSecret = "result-test-shared-value" //nolint:gosec // test fixture, not a credential

// lockedBuffer is written by server goroutines and read by the test.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	text := b.buf.String()
	b.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		out = append(out, record)
	}
	return out
}

func rpcRecords(t *testing.T, logs *lockedBuffer) []map[string]any {
	t.Helper()
	var found []map[string]any
	for _, record := range logs.records(t) {
		if record["msg"] == "rpc request" {
			found = append(found, record)
		}
	}
	return found
}

// waitRecord returns the one result line. The server writes it on its own
// goroutine, which can finish after a canceled client call returns.
func waitRecord(t *testing.T, logs *lockedBuffer) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(rpcRecords(t, logs)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// Let a duplicate line, if any, arrive before counting.
	time.Sleep(20 * time.Millisecond)
	found := rpcRecords(t, logs)
	if len(found) != 1 {
		t.Fatalf("rpc request records = %d, want 1: %v", len(found), logs.records(t))
	}
	return found[0]
}

func startResultServer(t *testing.T, handler rpcruntime.Handler) (*grpc.ClientConn, *lockedBuffer) {
	t.Helper()
	addr, logs := serveResult(t, handler)
	return dialResult(t, addr, resultTestSecret), logs
}

func dialResult(t *testing.T, addr, secret string) *grpc.ClientConn {
	t.Helper()
	conn, err := rpc.Dial(addr, secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func serveResult(t *testing.T, handler rpcruntime.Handler) (string, *lockedBuffer) {
	t.Helper()
	logs := &lockedBuffer{}
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := rpc.NewServer(logger.New(logs, "debug", "json"), resultTestSecret)
	runtimepb.RegisterRuntimeServiceServer(srv, rpcruntime.NewServer(map[string]rpcruntime.Handler{"probe": handler}))
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Stop)
	return listener.Addr().String(), logs
}

func callProbe(ctx context.Context, conn *grpc.ClientConn) error {
	_, err := runtimepb.NewRuntimeServiceClient(conn).Call(ctx, &runtimepb.CallRequest{Method: "probe", Payload: []byte("{}")})
	return err
}

func TestRPCResultLine(t *testing.T) {
	const method = runtimepb.RuntimeService_Call_FullMethodName
	tests := []struct {
		name      string
		handler   rpcruntime.Handler
		wantCode  codes.Code
		wantLevel string
		wantFault string
		wantError string
	}{
		{
			name:      "success",
			handler:   func(context.Context, json.RawMessage) (any, error) { return map[string]string{}, nil },
			wantCode:  codes.OK,
			wantLevel: "INFO",
		},
		{
			name: "client code",
			handler: func(context.Context, json.RawMessage) (any, error) {
				return nil, status.Error(codes.InvalidArgument, "channel.config_invalid")
			},
			wantCode:  codes.InvalidArgument,
			wantLevel: "INFO",
			wantFault: "client",
			wantError: "channel.config_invalid",
		},
		{
			name: "provider failure is a dependency failure",
			handler: func(context.Context, json.RawMessage) (any, error) {
				return nil, apperror.Wrap(apperror.CodeAgentProviderAuthFailed, errors.New("api error 401"), nil)
			},
			wantCode:  codes.Unauthenticated,
			wantLevel: "ERROR",
			wantFault: "dependency",
			wantError: "api error 401",
		},
		{
			name: "internal error keeps the cause the status hides",
			handler: func(context.Context, json.RawMessage) (any, error) {
				return nil, errors.New("database connection lost")
			},
			wantCode:  codes.Internal,
			wantLevel: "ERROR",
			wantFault: "server",
			wantError: "runtime method probe: database connection lost",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, logs := startResultServer(t, tt.handler)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if got := status.Code(callProbe(ctx, conn)); got != tt.wantCode {
				t.Fatalf("client code = %v, want %v", got, tt.wantCode)
			}
			record := waitRecord(t, logs)
			if record["level"] != tt.wantLevel {
				t.Fatalf("level = %v, want %s: %v", record["level"], tt.wantLevel, record)
			}
			if record["operation"] != method || record["grpc_code"] != tt.wantCode.String() {
				t.Fatalf("operation/grpc_code = %v/%v: %v", record["operation"], record["grpc_code"], record)
			}
			if record["component"] != "internal_rpc" {
				t.Fatalf("component = %v", record["component"])
			}
			if tt.wantFault == "" {
				if _, ok := record["error"]; ok {
					t.Fatalf("success record carries an error: %v", record)
				}
				return
			}
			if record["fault"] != tt.wantFault {
				t.Fatalf("fault = %v, want %s: %v", record["fault"], tt.wantFault, record)
			}
			if got, _ := record["error"].(string); !strings.Contains(got, tt.wantError) {
				t.Fatalf("error = %q, want it to contain %q", got, tt.wantError)
			}
		})
	}
}

// The client attributes a failure from the fault the server wrote into the
// envelope. The server has recorded the provider failure, so the client's
// record of it is a warning.
func TestRPCClientReadsTheServerFault(t *testing.T) {
	conn, _ := startResultServer(t, func(context.Context, json.RawMessage) (any, error) {
		return nil, apperror.Wrap(apperror.CodeAgentProviderRateLimited, errors.New("api error 429"), nil)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	restored := rpc.DecodeAppError(callProbe(ctx, conn))
	if got := apperror.CodeOf(restored); got != apperror.CodeAgentProviderRateLimited {
		t.Fatalf("code = %q", got)
	}
	result := errlog.Finish(ctx, "runtime.call", restored, errlog.Options{})
	if result.Level != slog.LevelWarn || result.Report.Fault != errs.FaultDependency || result.Report.RemoteFault != "dependency" {
		t.Fatalf("client result = level %v report %+v; want WARN dependency with remote_fault dependency", result.Level, result.Report)
	}
}

func TestRPCResultLineRecordsCanceledCall(t *testing.T) {
	entered := make(chan struct{})
	conn, logs := startResultServer(t, func(ctx context.Context, _ json.RawMessage) (any, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- callProbe(ctx, conn) }()
	<-entered
	cancel()
	if got := status.Code(<-errCh); got != codes.Canceled {
		t.Fatalf("client code = %v, want Canceled", got)
	}
	record := waitRecord(t, logs)
	if record["level"] != "INFO" || record["fault"] != "canceled" {
		t.Fatalf("level/fault = %v/%v, want INFO/canceled: %v", record["level"], record["fault"], record)
	}
	if got, _ := record["error"].(string); !strings.Contains(got, "context canceled") {
		t.Fatalf("error = %q", got)
	}
}

// A refusal by authentication is a call too; the result interceptor runs
// before the auth interceptor.
func TestRPCResultLineRecordsAuthRefusal(t *testing.T) {
	addr, logs := serveResult(t, func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	conn := dialResult(t, addr, "wrong-shared-value")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if got := status.Code(callProbe(ctx, conn)); got != codes.Unauthenticated {
		t.Fatalf("client code = %v, want Unauthenticated", got)
	}
	record := waitRecord(t, logs)
	if record["level"] != "INFO" || record["fault"] != "client" || record["grpc_code"] != codes.Unauthenticated.String() {
		t.Fatalf("level/fault/grpc_code = %v/%v/%v: %v", record["level"], record["fault"], record["grpc_code"], record)
	}
}

func TestRPCResultLineSkipsPassingHealthCheck(t *testing.T) {
	conn, logs := startResultServer(t, func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if found := rpcRecords(t, logs); len(found) != 0 {
		t.Fatalf("passing health check recorded: %v", found)
	}
}

// A stream is one unit of work: a stream the client cancels gets one line.
func TestRPCResultLineRecordsStream(t *testing.T) {
	conn, logs := startResultServer(t, func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	ctx, cancel := context.WithCancel(context.Background())
	watch, err := healthpb.NewHealthClient(conn).Watch(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := watch.Recv(); err != nil {
		t.Fatal(err)
	}
	cancel()
	record := waitRecord(t, logs)
	if record["operation"] != "/grpc.health.v1.Health/Watch" || record["grpc_code"] != codes.Canceled.String() {
		t.Fatalf("operation/grpc_code = %v/%v", record["operation"], record["grpc_code"])
	}
	if record["level"] != "INFO" || record["fault"] != "canceled" {
		t.Fatalf("level/fault = %v/%v: %v", record["level"], record["fault"], record)
	}
}

func TestUnaryServerResultRecordsPanicAndRepanics(t *testing.T) {
	logs := &lockedBuffer{}
	interceptor := rpc.UnaryServerResult(logger.New(logs, "debug", "json"))
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic was swallowed")
			}
		}()
		_, _ = interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Panics"},
			func(context.Context, any) (any, error) { panic("synthetic panic value") })
	}()
	record := waitRecord(t, logs)
	if record["level"] != "ERROR" || record["panic"] != true {
		t.Fatalf("level/panic = %v/%v: %v", record["level"], record["panic"], record)
	}
	if record["error"] != "panic" || record["error_stack"] == nil {
		t.Fatalf("error/error_stack = %v/%v", record["error"], record["error_stack"])
	}
}

// The caller's request id reaches the callee: its result line and what its
// handler logs report the id of the work that made the call, on a unary call
// and on a stream. A caller without one sends none.
func TestRPCCarriesTheCallerRequestID(t *testing.T) {
	var handlerID string
	conn, logs := startResultServer(t, func(ctx context.Context, _ json.RawMessage) (any, error) {
		handlerID = logger.RequestIDFromContext(ctx)
		return map[string]string{}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := callProbe(logger.ContextWithRequestID(ctx, "req-unary"), conn); err != nil {
		t.Fatal(err)
	}
	if record := waitRecord(t, logs); record["request_id"] != "req-unary" || handlerID != "req-unary" {
		t.Fatalf("result line request_id = %v, handler saw %q; want req-unary", record["request_id"], handlerID)
	}

	conn, logs = startResultServer(t, func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	streamCtx, streamCancel := context.WithCancel(logger.ContextWithRequestID(context.Background(), "req-stream"))
	watch, err := healthpb.NewHealthClient(conn).Watch(streamCtx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := watch.Recv(); err != nil {
		t.Fatal(err)
	}
	streamCancel()
	if record := waitRecord(t, logs); record["request_id"] != "req-stream" {
		t.Fatalf("stream result line request_id = %v, want req-stream", record["request_id"])
	}

	conn, logs = startResultServer(t, func(context.Context, json.RawMessage) (any, error) { return map[string]string{}, nil })
	if err := callProbe(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if record := waitRecord(t, logs); record["request_id"] != nil {
		t.Fatalf("call without a request id reports %v", record["request_id"])
	}
}
