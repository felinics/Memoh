package native

import (
	"context"

	sdk "github.com/felinics/twilight/sdk"
)

type providerStreamEventObserver struct {
	sdk.Provider
	observe func(StreamEvent)
}

func modelWithProviderStreamEventObserver(model *sdk.Model, observe func(StreamEvent)) *sdk.Model {
	if model == nil || model.Provider == nil || observe == nil {
		return model
	}
	observed := *model
	observed.Provider = providerStreamEventObserver{Provider: model.Provider, observe: observe}
	return &observed
}

func (p providerStreamEventObserver) DoStream(ctx context.Context, params sdk.GenerateParams) (*sdk.StreamResult, error) {
	// Every provider call begins a fresh attempt. For ordinary multi-step runs
	// the previous step has already consumed or checkpointed its timings; for a
	// retry this discards the failed attempt before replacement parts arrive.
	p.observe(StreamEvent{Type: EventRetry})
	result, err := p.Provider.DoStream(ctx, params)
	if err != nil || result == nil || result.Stream == nil {
		return result, err
	}

	source := result.Stream
	observed := make(chan sdk.StreamPart)
	result.Stream = observed
	go func() {
		defer close(observed)
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
			if event, ok := providerPartTimingEvent(part); ok {
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
