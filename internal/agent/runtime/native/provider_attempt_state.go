package native

import (
	"sync"

	sdk "github.com/felinics/twilight/sdk"
)

type providerAttemptState struct {
	mu              sync.RWMutex
	messages        []sdk.Message
	stepIndex       int
	systemPrepended bool
	provenance      preparedMessageProvenance
	stored          bool
}

func (s *providerAttemptState) store(
	params *sdk.GenerateParams,
	stepIndex int,
	systemPrepended bool,
	provenanceValues ...preparedMessageProvenance,
) {
	if s == nil || params == nil {
		return
	}
	var provenance preparedMessageProvenance
	if len(provenanceValues) > 0 {
		provenance = provenanceValues[0]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = cloneProviderMessages(params.Messages)
	s.stepIndex = stepIndex
	s.systemPrepended = systemPrepended
	s.provenance = clonePreparedMessageProvenance(provenance)
	s.stored = true
}

func (s *providerAttemptState) retryInput(previous *sdk.StreamResult) (providerRetryInput, bool) {
	if s == nil {
		return providerRetryInput{}, false
	}
	s.mu.RLock()
	messages := cloneProviderMessages(s.messages)
	stepIndex := s.stepIndex
	systemPrepended := s.systemPrepended
	provenance := clonePreparedMessageProvenance(s.provenance)
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
		if provenance.known {
			if len(provenance.messageIndexes) == 0 {
				return providerRetryInput{}, false
			}
			provenance.messageIndexes = provenance.messageIndexes[1:]
		}
	}
	clearProviderCacheControls(messages)
	if previous != nil && stepIndex >= 0 && stepIndex < len(previous.Steps) {
		stepMessages := cloneProviderMessages(previous.Steps[stepIndex].Messages)
		messages = append(messages, stepMessages...)
		if provenance.known {
			for range stepMessages {
				provenance.messageIndexes = append(provenance.messageIndexes, -1)
			}
		}
	}
	return providerRetryInput{messages: messages, provenance: provenance}, true
}

func (s *providerAttemptState) retryMessages(previous *sdk.StreamResult) ([]sdk.Message, bool) {
	input, ok := s.retryInput(previous)
	return input.messages, ok
}

type providerRetryInput struct {
	messages   []sdk.Message
	provenance preparedMessageProvenance
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

func retryProviderAttemptMessages(cfg RunConfig, previous *sdk.StreamResult) providerRetryInput {
	if input, ok := cfg.providerAttemptState.retryInput(previous); ok {
		return input
	}
	accumulated := []sdk.Message(nil)
	if previous != nil {
		accumulated = previous.Messages
	}
	merged := make([]sdk.Message, 0, len(cfg.Messages)+len(accumulated))
	merged = append(merged, cfg.Messages...)
	merged = append(merged, accumulated...)
	provenance := clonePreparedMessageProvenance(cfg.providerMessageProvenance)
	if provenance.known {
		for range accumulated {
			provenance.messageIndexes = append(provenance.messageIndexes, -1)
		}
	}
	return providerRetryInput{messages: merged, provenance: provenance}
}
