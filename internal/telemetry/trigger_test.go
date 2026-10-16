package telemetry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/felinics/memoh/internal/telemetry"
)

// Work that outlives what asked for it must not be a child of it.
//
// A turn sent over a WebSocket, or picked off the inbound queue, runs after
// the request that delivered it has been answered. Nesting it under that
// request produces a parent that ends before its child, and — on a connection
// that stays open — one trace covering every turn of the conversation, so
// asking how long an answer took returns the length of the session.
func TestDetachedWorkStartsItsOwnTraceLinkedToItsCause(t *testing.T) {
	recorder := recordSpans(t)

	callerCtx, caller := telemetry.Tracer().Start(context.Background(), "ws.handshake")
	trigger := telemetry.TriggerFrom(callerCtx)
	caller.End()

	_, span := telemetry.StartLinked(context.Background(), trigger, "agent.turn")
	span.End()

	turnSpan := findSpanNamed(t, recorder, "agent.turn")
	if turnSpan.Parent().IsValid() {
		t.Errorf("turn has parent %s, want a root span", turnSpan.Parent().SpanID())
	}
	if turnSpan.SpanContext().TraceID() == caller.SpanContext().TraceID() {
		t.Error("turn shares the caller's trace id, so the two cannot be told apart")
	}
	links := turnSpan.Links()
	if len(links) != 1 {
		t.Fatalf("links = %d, want 1 naming the cause", len(links))
	}
	if links[0].SpanContext.SpanID() != caller.SpanContext().SpanID() {
		t.Errorf("link points at %s, want the caller %s",
			links[0].SpanContext.SpanID(), caller.SpanContext().SpanID())
	}
}

// A context that already carries a span is not enough to make the new span a
// child of it: on a WebSocket the connection's context is the one every turn
// is started from, and adopting it is the defect.
func TestDetachedWorkIgnoresTheSpanAlreadyInTheContext(t *testing.T) {
	recorder := recordSpans(t)

	ambientCtx, ambient := telemetry.Tracer().Start(context.Background(), "connection")
	defer ambient.End()

	_, span := telemetry.StartLinked(ambientCtx, telemetry.Trigger{}, "agent.turn")
	span.End()

	turnSpan := findSpanNamed(t, recorder, "agent.turn")
	if turnSpan.Parent().IsValid() {
		t.Errorf("turn has parent %s, want a root span", turnSpan.Parent().SpanID())
	}
	if turnSpan.SpanContext().TraceID() == ambient.SpanContext().TraceID() {
		t.Error("turn joined the ambient trace")
	}
}

// The other half of the rule, and the reason this is not an unconditional
// WithNewRoot: a call that arrives over the internal RPC really is inside its
// caller's request, and that trace has to stay continuous across the hop.
// Without this case the asymmetry could be satisfied by detaching everything.
func TestWorkWithNoRecordedCauseStaysInTheCallersTrace(t *testing.T) {
	recorder := recordSpans(t)

	callerCtx, caller := telemetry.Tracer().Start(context.Background(), "internal.rpc")
	defer caller.End()

	_, span := telemetry.StartDetached(callerCtx, "agent.turn")
	span.End()

	turnSpan := findSpanNamed(t, recorder, "agent.turn")
	if turnSpan.Parent().SpanID() != caller.SpanContext().SpanID() {
		t.Errorf("parent = %s, want the caller %s", turnSpan.Parent().SpanID(), caller.SpanContext().SpanID())
	}
}

// A trigger travels in a queue item, so it has to survive being carried
// somewhere a context cannot go, and a context carrying one has to be usable
// as the base for work that starts later.
func TestTriggerCarriedInAContextDetachesTheWorkStartedFromIt(t *testing.T) {
	recorder := recordSpans(t)

	callerCtx, caller := telemetry.Tracer().Start(context.Background(), "ws.handshake")
	caller.End()

	base := telemetry.ContextWithTrigger(context.Background(), telemetry.TriggerFrom(callerCtx))
	_, span := telemetry.StartDetached(base, "agent.turn")
	span.End()

	turnSpan := findSpanNamed(t, recorder, "agent.turn")
	if turnSpan.Parent().IsValid() {
		t.Errorf("turn has parent %s, want a root span", turnSpan.Parent().SpanID())
	}
	if len(turnSpan.Links()) != 1 {
		t.Fatalf("links = %d, want 1 naming the handshake", len(turnSpan.Links()))
	}
}

// ContextWithTrigger has to clear the span as well as record the trigger.
// A context that still carries the connection's span would hand it to
// anything that does not go through StartDetached — a database query, an
// outbound HTTP call — and those would land in the connection's trace.
func TestContextWithTriggerClearsTheAmbientSpan(t *testing.T) {
	// A provider has to be installed: without one the tracer is a no-op whose
	// span context is invalid anyway, and the assertion below would hold for
	// a ContextWithTrigger that cleared nothing.
	recordSpans(t)

	ambientCtx, ambient := telemetry.Tracer().Start(context.Background(), "connection")
	defer ambient.End()

	base := telemetry.ContextWithTrigger(ambientCtx, telemetry.Trigger{})
	if sc := trace.SpanContextFromContext(base); sc.IsValid() {
		t.Errorf("context still carries span %s", sc.SpanID())
	}
}

// A WebSocket handler does not return until the socket closes. A span around
// it would report how long a tab was open, stay open for that whole time, and
// be the current span while it was — adopting every turn sent over the
// connection into one trace.
func TestEchoServerDoesNotSpanAWebSocketUpgrade(t *testing.T) {
	recorder := recordSpans(t)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/bots/abc", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	rec := serve(t, req, func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}

	for _, span := range recorder.Ended() {
		if span.Name() == "GET /bots/:id" {
			t.Errorf("upgrade produced a server span named %q", span.Name())
		}
	}
}

// An ordinary request on the same route still gets its span, so the skip is
// about upgrades rather than about the route.
func TestEchoServerStillSpansAnOrdinaryRequest(t *testing.T) {
	recorder := recordSpans(t)

	rec := serve(t, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/bots/abc", nil),
		func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}

	findSpanNamed(t, recorder, "GET /bots/:id")
}

// findSpanNamed returns the one recorded span with that name.
func findSpanNamed(t *testing.T, recorder *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	var names []string
	for _, span := range recorder.Ended() {
		if span.Name() == name {
			return span
		}
		names = append(names, span.Name())
	}
	t.Fatalf("no span named %q; got %v", name, names)
	return nil
}
