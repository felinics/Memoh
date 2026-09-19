package native

import (
	"context"

	sdk "github.com/felinics/twilight/sdk"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/felinics/memoh/internal/telemetry"
)

// Span names for model calls. They are deliberately literal about what each
// one measures, because the two are not the same duration and reading them as
// if they were would be worse than having neither.
const (
	// spanModelStreamStart covers establishing the stream, including retries,
	// and ends when the provider accepts the request — not when the reply is
	// finished. That interval is what a user experiences as the wait before
	// anything appears, and it is the one a provider problem shows up in.
	spanModelStreamStart = "agent.model.stream_start"
	// spanModelGenerate covers a whole non-streaming call, so this one is the
	// full generation.
	spanModelGenerate = "agent.model.generate"
)

// traceModelCall starts a span around a call to the model provider.
//
// Nothing about the conversation is recorded: not the prompt, not the reply,
// not the tool definitions sent with it. A span carrying those would publish
// the user's messages to whoever can read traces, which is the same rule
// docs/logging.md applies to log records. The model name and the outcome are
// what makes the span useful.
func traceModelCall(ctx context.Context, name string, model *sdk.Model) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{}
	if model != nil {
		attrs = append(attrs, attribute.String("agent.model.id", model.ID))
		if model.Provider != nil {
			attrs = append(attrs, attribute.String("agent.model.provider", model.Provider.Name()))
		}
	}
	return telemetry.Tracer().Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...),
	)
}

// endModelCall closes a model span, recording the error and how many attempts
// it took. Retries are invisible in the logs today: a provider that fails
// twice and succeeds on the third try looks the same as one that answered
// immediately, except slower.
func endModelCall(span trace.Span, attempts int, err error) {
	if attempts > 1 {
		span.SetAttributes(attribute.Int("agent.model.attempts", attempts))
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "")
	}
	span.End()
}
