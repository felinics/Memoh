package native

import "context"

type feedbackIndexesKey struct{}

// InternalFeedbackIndexes names internal model inputs in the decorated step.
// The sidecar stays outside SDK messages: model roles describe the provider
// protocol, not whether a person opened a new conversation turn.
func InternalFeedbackIndexes(ctx context.Context) []int {
	indexes, _ := ctx.Value(feedbackIndexesKey{}).([]int)
	return append([]int(nil), indexes...)
}

// withMessageOrigins marks the read-media carriers among the dynamic inputs
// admitted for durable step stepIndex. The indexes are positions in the
// decorated step's message list, where the admitted inputs lead the step's
// own output (decorateCommittedStep).
func (d *loopDynamicInputs) withMessageOrigins(ctx context.Context, stepIndex int) context.Context {
	if d == nil {
		return ctx
	}
	return context.WithValue(ctx, feedbackIndexesKey{}, d.feedbackIndexes(stepIndex))
}

// feedbackIndexes lists, in decorated-message order, the admitted read-media
// carriers that were input to durable step stepIndex.
func (d *loopDynamicInputs) feedbackIndexes(stepIndex int) []int {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var indexes []int
	ordinal := 0
	for i := range d.records {
		if d.records[i].boundary != stepIndex || !d.records[i].admitted {
			continue
		}
		if d.records[i].readMedia {
			indexes = append(indexes, ordinal)
		}
		ordinal++
	}
	return indexes
}
