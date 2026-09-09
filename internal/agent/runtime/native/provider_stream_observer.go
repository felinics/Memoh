package native

import (
	"context"

	sdk "github.com/felinics/twilight/sdk"
)

type providerStreamEventObserver struct {
	sdk.Provider
	observe func(StreamEvent)
	clock   *stepClock
}

func modelWithProviderStreamObserver(model *sdk.Model, observe func(StreamEvent), clock *stepClock) *sdk.Model {
	if model == nil || model.Provider == nil || (observe == nil && clock == nil) {
		return model
	}
	if observe == nil {
		observe = func(StreamEvent) {}
	}
	observed := *model
	observed.Provider = providerStreamEventObserver{Provider: model.Provider, observe: observe, clock: clock}
	return &observed
}

func (p providerStreamEventObserver) DoStream(ctx context.Context, params sdk.GenerateParams) (*sdk.StreamResult, error) {
	// Every provider call begins a fresh attempt. For ordinary multi-step runs
	// the previous step has already consumed or checkpointed its timings; for a
	// retry this discards the failed attempt before replacement parts arrive.
	p.observe(StreamEvent{Type: EventRetry})
	startedAt := p.clock.begin()
	p.observe(StreamEvent{Type: EventStepStart, Timing: &StepTiming{StartedAtMS: startedAt}})
	result, err := p.Provider.DoStream(ctx, params)
	if err != nil || result == nil || result.Stream == nil {
		p.clock.abandon()
		return result, err
	}

	source := result.Stream
	observed := make(chan sdk.StreamPart)
	result.Stream = observed
	go func() {
		defer close(observed)
		// Read like Twilight ranges the provider stream: a cancelled context
		// stops forwarding, never reading, so the provider always completes
		// the send it is blocked on and observes the cancellation itself.
		for part := range source {
			if event, ok := p.partEvent(part); ok {
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

func (p providerStreamEventObserver) partEvent(part sdk.StreamPart) (StreamEvent, bool) {
	switch v := part.(type) {
	case *sdk.TextDeltaPart:
		p.clock.firstTokenText(v.Text)
	case *sdk.ReasoningDeltaPart:
		p.clock.firstTokenText(v.Text)
	case *sdk.ToolInputStartPart, *sdk.StreamToolCallPart:
		p.clock.firstToken()
	case *sdk.FinishStepPart:
		usage := normalizeProviderUsage(p.Name(), v.Usage)
		completed, ok := p.clock.finish(usage, v.FinishReason)
		if !ok {
			return StreamEvent{}, false
		}
		timing := completed.Timing
		return StreamEvent{
			Type:         EventStepEnd,
			FinishReason: string(v.FinishReason),
			Usage:        marshalUsage(usage),
			Timing:       &timing,
		}, true
	}
	return providerPartTimingEvent(part)
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
