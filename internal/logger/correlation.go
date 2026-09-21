package logger

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// Keys the correlation handler adds. They are snake_case like every other key
// in the output: a log record is not an OpenTelemetry attribute set, and most
// log pipelines flatten dots to underscores on ingest anyway, so a dotted
// spelling here would be written one way and queried another.
const (
	requestIDKey = "request_id"
	traceIDKey   = "trace_id"
	spanIDKey    = "span_id"
)

type requestIDContextKey struct{}

// ContextWithRequestID returns ctx carrying id, so that every record logged
// with that context reports it.
//
// The request identifier already leaves this process in error responses
// (apperror.Problem carries request_id). Putting it on log records is what
// makes an identifier a user can quote back useful: without it, the
// identifier names something the logs cannot find.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDContextKey{}, id)
}

// RequestIDFromContext reports the request identifier ctx carries, or "".
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}

// correlationHandler adds request and trace identity to every record from the
// context the record was logged with.
//
// This is what Handler.Handle's context parameter is for: reading values that
// are already in the context rather than having each call site repeat them.
// The alternative — a logger stored in the context — is the shape slog
// deliberately does not offer.
type correlationHandler struct{ inner slog.Handler }

func (h correlationHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h correlationHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestIDFromContext(ctx); id != "" {
		r.AddAttrs(slog.String(requestIDKey, id))
	}
	// A span context is only valid once tracing is configured and the caller
	// passed the request's context. Absent either, no trace keys are emitted
	// rather than empty ones, so a query for trace_id never matches a record
	// that has no trace.
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String(traceIDKey, sc.TraceID().String()),
			slog.String(spanIDKey, sc.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, r)
}

// WithAttrs and WithGroup must rebuild this type around the derived inner
// handler. Returning h.inner directly — which is what embedding slog.Handler
// and overriding only Handle would do — drops this handler from every logger
// derived with With or WithGroup. Nothing fails: the code compiles, the root
// logger still reports correlation, and every derived logger silently stops.
// This codebase derives a logger with With in 166 places to attach a
// component name, so that failure would cover almost all of its output.
func (h correlationHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return correlationHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h correlationHandler) WithGroup(name string) slog.Handler {
	return correlationHandler{inner: h.inner.WithGroup(name)}
}
