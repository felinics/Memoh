package application

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/felinics/memoh/internal/telemetry"
)

// startTurnSpan opens the span that covers one agent turn and returns the
// function that closes it.
//
// It lives here, on the two functions that actually run a turn, rather than on
// the turn port's StartTurn. StartTurn is only one of the two ways in: the web
// UI's WebSocket goes through streamChatWSResultWithHooks and never touches
// it. A span on StartTurn alone would be missing for the entry point most
// turns arrive through, and nothing would say so — the traces would simply
// have no turn in them.
//
// The turn is the only span that says how long an answer took. Everything
// below it — the model call, each tool call, the queries they make — is a
// fragment of that number.
func startTurnSpan(ctx context.Context, req ChatRequest) (context.Context, func(error)) {
	ctx, span := telemetry.Tracer().Start(ctx, "agent.turn",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("agent.bot_id", req.BotID),
			attribute.String("agent.thread_id", req.ThreadID),
		),
	)
	return ctx, func(err error) {
		switch {
		// A run someone stopped is not a failure. Marking cancellations as
		// errors would make the error rate track how often users press stop.
		case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
			span.SetAttributes(attribute.String("agent.turn.outcome", "aborted"))
		case err != nil:
			span.SetAttributes(attribute.String("agent.turn.outcome", "errored"))
			span.RecordError(err)
			span.SetStatus(codes.Error, "")
		default:
			span.SetAttributes(attribute.String("agent.turn.outcome", "completed"))
		}
		span.End()
	}
}
