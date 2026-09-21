package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Trigger names the work that caused a detached operation, without joining it.
//
// A turn does not run inside the request that asked for it. The HTTP handler
// answers immediately and a worker picks the message up later, or the message
// arrives on a WebSocket that stays open for hours. Making the turn a child of
// either produces a trace that is wrong in a specific way: a parent that ends
// before its children, or one that never ends at all, and in both cases every
// turn on that connection shares one trace id, so asking "how long did this
// answer take" returns the length of the conversation.
//
// A link says what caused the work without claiming it contains it. The two
// traces stay separately queryable and each has an honest duration, and a
// reader who starts at either end can still reach the other.
//
// The zero Trigger links nothing, which is what a turn started with no
// traceable cause should do.
type Trigger struct {
	traceparent string
	tracestate  string
}

// TriggerFrom captures the operation ctx is currently in.
//
// It is a serialized form rather than a trace.SpanContext because the value
// travels in a queue item next to the message: the producer and the consumer
// are different goroutines with unrelated lifetimes, and a context is not the
// thing to hand across that gap.
func TriggerFrom(ctx context.Context) Trigger {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return Trigger{traceparent: carrier["traceparent"], tracestate: carrier["tracestate"]}
}

// StartLinked begins a new trace for name, linked to the operation that caused
// it. It is a root even when ctx already carries a span: the caller's span is
// the thing being deliberately not joined.
func StartLinked(ctx context.Context, trigger Trigger, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	opts = append([]trace.SpanStartOption{trace.WithNewRoot()}, opts...)
	if link := trigger.spanContext(); link.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: link}))
	}
	return Tracer().Start(ctx, name, opts...)
}

func (t Trigger) spanContext() trace.SpanContext {
	if t.traceparent == "" {
		return trace.SpanContext{}
	}
	extracted := otel.GetTextMapPropagator().Extract(context.Background(), propagation.MapCarrier{
		"traceparent": t.traceparent,
		"tracestate":  t.tracestate,
	})
	return trace.SpanContextFromContext(extracted)
}

type triggerContextKey struct{}

// ContextWithTrigger returns ctx carrying trigger and no current span.
//
// Both halves matter. The trigger is how a span started later in this context
// says what caused it; dropping the current span is what stops that span from
// being adopted into the caller's trace, which is the whole point on a
// connection whose own span has already ended.
func ContextWithTrigger(ctx context.Context, trigger Trigger) context.Context {
	ctx = trace.ContextWithSpanContext(ctx, trace.SpanContext{})
	return context.WithValue(ctx, triggerContextKey{}, trigger)
}

// TriggerFromContext reports the trigger ctx carries, or the zero Trigger.
func TriggerFromContext(ctx context.Context) Trigger {
	trigger, _ := ctx.Value(triggerContextKey{}).(Trigger)
	return trigger
}

// StartDetached begins a span for work whose cause is recorded in ctx by
// ContextWithTrigger, and an ordinary child span when it is not.
//
// The two cases are both real and must both work. A turn arriving over a
// WebSocket or off a queue is detached from whatever asked for it, and links
// to it. A turn arriving over the internal RPC — Channel calling Server in
// split mode — is genuinely inside its caller's request, and the trace has to
// stay continuous across that hop.
func StartDetached(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if trigger := TriggerFromContext(ctx); trigger.spanContext().IsValid() {
		return StartLinked(ctx, trigger, name, opts...)
	}
	return Tracer().Start(ctx, name, opts...)
}
