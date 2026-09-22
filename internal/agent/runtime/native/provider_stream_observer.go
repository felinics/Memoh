package native

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/felinics/memoh/internal/telemetry"
)

type providerCallObserver struct {
	sdk.Provider
	observe func(StreamEvent)
	steer   *modelSteerGate
	// model names the call for the span. It is captured here rather than read
	// from GenerateParams because the provider is handed params, not the model
	// record the rest of the runtime knows the call by.
	model modelIdentity
	// calls counts provider calls within one run, so a waterfall says which
	// round a span belongs to. It is a pointer because this struct is stored
	// in an interface and copied by value on every call.
	calls *atomic.Int64
}

type modelIdentity struct {
	id       string
	provider string
}

func modelWithProviderCallObserver(model *sdk.Model, observe func(StreamEvent), steer *modelSteerGate) *sdk.Model {
	if model == nil || model.Provider == nil {
		return model
	}
	observed := *model
	provider := model.Provider
	// A final-steer continuation reuses the model from the preceding call.
	// Replace our observer instead of nesting another stream/notification loop.
	for {
		previous, ok := provider.(providerCallObserver)
		if !ok {
			break
		}
		provider = previous.Provider
	}
	identity := modelIdentity{id: model.ID}
	if provider != nil {
		identity.provider = provider.Name()
	}
	observed.Provider = providerCallObserver{
		Provider: provider,
		observe:  observe,
		steer:    steer,
		model:    identity,
		calls:    &atomic.Int64{},
	}
	return &observed
}

// startCallSpan opens the span for one provider call and returns it with the
// attributes every outcome shares.
//
// Unlike the rest of the runtime this hands the span's context to the layer
// below. That is safe here and nowhere above: the wrapped provider only makes
// the HTTP request, it does not run tools, so nothing the SDK executes can
// end up parented to a model span.
func (p providerCallObserver) startCallSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	index := p.calls.Add(1) - 1
	ctx, span := telemetry.Tracer().Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("agent.model.id", p.model.id),
			attribute.String("agent.model.provider", p.model.provider),
			attribute.Int64("agent.model.call_index", index),
		),
	)
	return ctx, span
}

// endCallSpan closes a call span, naming how the call ended.
//
// A cancelled call is not a failure: a user who pressed stop, and a steer that
// replaced the in-flight answer, both cancel the context. Recording those as
// errors would make the model error rate track how often people change their
// mind, which is the same reasoning turn_tracing.go applies to the turn.
func endCallSpan(ctx context.Context, span trace.Span, err error) {
	switch {
	case errors.Is(context.Cause(ctx), errModelSteered):
		span.SetAttributes(attribute.String("agent.model.outcome", "steered"))
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		span.SetAttributes(attribute.String("agent.model.outcome", "aborted"))
	case err != nil:
		span.SetAttributes(attribute.String("agent.model.outcome", "errored"))
		telemetry.RecordFailure(span, err)
	default:
		span.SetAttributes(attribute.String("agent.model.outcome", "completed"))
	}
	span.End()
}

func (p providerCallObserver) DoGenerate(ctx context.Context, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
	ctx, span := p.startCallSpan(ctx, spanModelGenerate)
	result, err := p.Provider.DoGenerate(ctx, params)
	endCallSpan(ctx, span, err)
	return result, err
}

func (p providerCallObserver) DoStream(ctx context.Context, params sdk.GenerateParams) (*sdk.StreamResult, error) {
	// Every provider call begins a fresh attempt. For ordinary multi-step runs
	// the previous step has already consumed or checkpointed its timings; for a
	// retry this discards the failed attempt before replacement parts arrive.
	if p.observe != nil {
		p.observe(StreamEvent{Type: EventRetry})
	}
	ctx, span := p.startCallSpan(ctx, spanModelStream)
	started := time.Now()
	p.steer.begin()
	result, err := p.Provider.DoStream(ctx, params)
	if err != nil || result == nil || result.Stream == nil {
		endCallSpan(ctx, span, err)
		return result, err
	}
	// Nothing to watch for and nothing to record: hand the provider's own
	// channel straight back rather than paying for a goroutine and a hop on
	// every part of every call.
	if p.observe == nil && p.steer == nil && !span.IsRecording() {
		span.End()
		return result, nil
	}

	source := result.Stream
	observed := make(chan sdk.StreamPart)
	result.Stream = observed
	go func() {
		var streamErr error
		sawFirstPart := false
		defer func() {
			endCallSpan(ctx, span, streamErr)
			close(observed)
		}()
		for {
			var part sdk.StreamPart
			var ok bool
			select {
			case part, ok = <-source:
				if !ok {
					return
				}
			case <-ctx.Done():
				return
			}
			if !p.steer.observe(part) {
				return
			}
			// The wait before the provider says anything is the pause a user
			// sits through, and it is not the same number as the call: an
			// answer that streams tool arguments for twenty seconds spends
			// almost none of that waiting. It is recorded here rather than as
			// a span of its own because one call producing two nested spans
			// would double every model row in a waterfall.
			//
			// Measured at this layer the number is the provider's, not ours:
			// the SDK buffers parts 64 deep before the runtime sees them.
			if !sawFirstPart && partCarriesContent(part) {
				sawFirstPart = true
				span.SetAttributes(attribute.Int64("agent.model.first_part_ms", time.Since(started).Milliseconds()))
			}
			if e, ok := part.(*sdk.ErrorPart); ok && e.Error != nil {
				streamErr = e.Error
			}
			if event, ok := providerPartTimingEvent(part); ok && p.observe != nil {
				p.observe(event)
			}
			select {
			case observed <- part:
			case <-ctx.Done():
				return
			}
		}
	}()
	return result, nil
}

// partCarriesContent reports whether the provider has said something.
//
// Start parts and the step bookkeeping around them are emitted as soon as
// there is a goroutine to emit them, so treating those as the first part would
// time our own plumbing — which is what the first two attempts at this
// measurement did.
func partCarriesContent(part sdk.StreamPart) bool {
	switch part.(type) {
	case *sdk.TextStartPart, *sdk.TextDeltaPart,
		*sdk.ReasoningStartPart, *sdk.ReasoningDeltaPart,
		*sdk.ToolInputStartPart, *sdk.StreamToolCallPart:
		return true
	default:
		return false
	}
}

func providerPartTimingEvent(part sdk.StreamPart) (StreamEvent, bool) {
	switch p := part.(type) {
	case *sdk.ReasoningStartPart:
		return StreamEvent{Type: EventReasoningStart}, true
	case *sdk.ReasoningDeltaPart:
		return StreamEvent{Type: EventReasoningDelta, Delta: p.Text}, true
	case *sdk.ReasoningEndPart:
		return StreamEvent{Type: EventReasoningEnd}, true
	case *sdk.TextStartPart:
		return StreamEvent{Type: EventTextStart}, true
	case *sdk.TextDeltaPart:
		return StreamEvent{Type: EventTextDelta, Delta: p.Text}, true
	case *sdk.TextEndPart:
		return StreamEvent{Type: EventTextEnd}, true
	case *sdk.ToolInputStartPart:
		return StreamEvent{Type: EventToolCallInputStart}, true
	case *sdk.StreamToolCallPart:
		return StreamEvent{Type: EventToolCallStart}, true
	case *sdk.ToolProgressPart:
		return StreamEvent{Type: EventToolCallProgress}, true
	case *sdk.ToolApprovalRequestPart:
		return StreamEvent{Type: EventToolApprovalRequest}, true
	case *sdk.AbortPart:
		return StreamEvent{Type: EventAgentAbort}, true
	default:
		return StreamEvent{}, false
	}
}
