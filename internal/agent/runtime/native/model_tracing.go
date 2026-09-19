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
	// spanModelFirstPart covers the wait before the provider says anything:
	// it starts when the request is built, includes retries, and ends when
	// the first part of the reply arrives. That is the pause a user sits
	// through, and the one a provider problem shows up in.
	//
	// It deliberately does not cover the rest of the reply. Naming the whole
	// generation would hide the number that matters inside one that is
	// dominated by how long the answer happened to be.
	spanModelFirstPart = "agent.model.first_part"
	// spanModelGenerate covers a whole non-streaming call, so this one is the
	// full generation.
	spanModelGenerate = "agent.model.generate"
	// spanModelRetry covers restarting a stream that broke partway through.
	// It ends when the new stream is handed back rather than at its first
	// part: the reply is already in progress, so there is no user-visible
	// pause to measure, only whether the restart worked.
	spanModelRetry = "agent.model.stream_retry"
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
