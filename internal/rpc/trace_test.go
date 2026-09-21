package rpc_test

import (
	"context"
	"net"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/felinics/memoh/internal/rpc"
	"github.com/felinics/memoh/internal/telemetry"
)

// A trace that stops at a process boundary is the failure this guards. Both
// ends look instrumented — each produces spans — and the only symptom is that
// the two halves of one request appear as two unrelated traces. Nothing logs
// an error when it happens, so only an end-to-end assertion catches it.
func TestTraceContinuesAcrossTheInternalRPCBoundary(t *testing.T) {
	// Not a credential: the internal RPC requires a shared secret, and this
	// is the value both ends of this test agree on.
	const sharedSecret = "trace-test-shared-value" //nolint:gosec // test fixture, not a credential

	recorder := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	otel.SetTextMapPropagator(propagation.TraceContext{})

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := rpc.NewServer(sharedSecret)
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Stop)

	conn, err := rpc.Dial(listener.Addr().String(), sharedSecret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// A caller-side span, as a request handler would already have.
	ctx, caller := telemetry.Tracer().Start(context.Background(), "caller")
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := healthpb.NewHealthClient(conn).Check(callCtx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatal(err)
	}
	caller.End()

	client, server := findSpanPair(t, recorder)
	if client.SpanContext().TraceID() != server.SpanContext().TraceID() {
		t.Fatalf("trace id differs across the boundary: client %s, server %s",
			client.SpanContext().TraceID(), server.SpanContext().TraceID())
	}
	if server.Parent().SpanID() != client.SpanContext().SpanID() {
		t.Fatalf("server span parent = %s, want the client span %s",
			server.Parent().SpanID(), client.SpanContext().SpanID())
	}
	if got := client.SpanContext().TraceID(); got != caller.SpanContext().TraceID() {
		t.Fatalf("client span started a new trace %s instead of continuing %s",
			got, caller.SpanContext().TraceID())
	}
}

// findSpanPair waits for the client and server spans of one call. The server
// span ends on the server goroutine, so it can arrive after the call returns.
func findSpanPair(t *testing.T, recorder *tracetest.SpanRecorder) (client, server sdktrace.ReadOnlySpan) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		client, server = nil, nil
		for _, span := range recorder.Ended() {
			switch span.SpanKind() {
			case trace.SpanKindClient:
				client = span
			case trace.SpanKindServer:
				server = span
			}
		}
		if client != nil && server != nil {
			return client, server
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing span after 5s: client=%v server=%v", client != nil, server != nil)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
