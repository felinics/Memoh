package native

import (
	"sync"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/step"
)

type providerAttemptState struct {
	mu              sync.RWMutex
	messages        []sdk.Message
	stepIndex       int
	systemPrepended bool
	// dynamicRefs are the loop-owned dynamic messages the stored payload
	// carried, positioned within the stored messages.
	dynamicRefs []dynamicSourceRef
	stored      bool
}

func (s *providerAttemptState) store(
	params *sdk.Request,
	stepIndex int,
	systemPrepended bool,
	dynamicRefs []dynamicSourceRef,
) {
	if s == nil || params == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = cloneProviderMessages(params.Messages)
	s.stepIndex = stepIndex
	s.systemPrepended = systemPrepended
	s.dynamicRefs = cloneDynamicSourceRefs(dynamicRefs)
	s.stored = true
}

// retryInput rebuilds the provider input for a retried dispatch from the
// stored attempt payload plus the output of the step the attempt reached.
// previousSteps are the steps the current dispatch already committed, in
// call-local order.
func (s *providerAttemptState) retryInput(previousSteps []step.Record) (providerRetryInput, bool) {
	if s == nil {
		return providerRetryInput{}, false
	}
	s.mu.RLock()
	messages := cloneProviderMessages(s.messages)
	stepIndex := s.stepIndex
	systemPrepended := s.systemPrepended
	dynamicRefs := cloneDynamicSourceRefs(s.dynamicRefs)
	stored := s.stored
	s.mu.RUnlock()
	if !stored {
		return providerRetryInput{}, false
	}

	if systemPrepended {
		if len(messages) == 0 {
			return providerRetryInput{}, false
		}
		messages = messages[1:]
		dynamicRefs = shiftDynamicSourceRefs(dynamicRefs, -1)
	}
	clearProviderCacheControls(messages)
	if stepIndex >= 0 && stepIndex < len(previousSteps) {
		stepMessages := cloneProviderMessages(previousSteps[stepIndex].Messages)
		messages = append(messages, stepMessages...)
	}
	return providerRetryInput{messages: messages, dynamicRefs: dynamicRefs}, true
}

func (s *providerAttemptState) retryMessages(previousSteps []step.Record) ([]sdk.Message, bool) {
	input, ok := s.retryInput(previousSteps)
	return input.messages, ok
}

type providerRetryInput struct {
	messages    []sdk.Message
	dynamicRefs []dynamicSourceRef
}

func cloneProviderMessages(messages []sdk.Message) []sdk.Message {
	if messages == nil {
		return nil
	}
	cloned := make([]sdk.Message, len(messages))
	for i := range messages {
		cloned[i] = messages[i]
		cloned[i].Content = append([]sdk.MessagePart(nil), messages[i].Content...)
		if messages[i].Usage != nil {
			usage := *messages[i].Usage
			cloned[i].Usage = &usage
		}
	}
	return cloned
}

func clearProviderCacheControls(messages []sdk.Message) {
	for i := range messages {
		for j, part := range messages[i].Content {
			switch value := part.(type) {
			case sdk.TextPart:
				value.CacheControl = nil
				messages[i].Content[j] = value
			case sdk.ImagePart:
				value.CacheControl = nil
				messages[i].Content[j] = value
			case sdk.FilePart:
				value.CacheControl = nil
				messages[i].Content[j] = value
			}
		}
	}
}

// retryProviderAttemptMessages falls back to durable history when no provider
// attempt was stored: the segment's committed steps plus its output messages.
func retryProviderAttemptMessages(cfg RunConfig, steps []step.Record, messages []sdk.Message) providerRetryInput {
	if input, ok := cfg.providerAttemptState.retryInput(steps); ok {
		return input
	}
	merged := make([]sdk.Message, 0, len(cfg.Messages)+len(messages))
	merged = append(merged, cfg.Messages...)
	merged = append(merged, messages...)
	return providerRetryInput{messages: merged, dynamicRefs: cloneDynamicSourceRefs(cfg.retryDynamicRefs)}
}
