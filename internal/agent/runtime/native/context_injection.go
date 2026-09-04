package native

import (
	"maps"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/event"
)

// StampContextInjection marks the first text part of a runtime-injected user
// message with its kind so persistence can label the row wherever it lands.
func StampContextInjection(message sdk.Message, kind string) sdk.Message {
	for i, part := range message.Content {
		text, ok := part.(sdk.TextPart)
		if !ok {
			continue
		}
		metadata := make(map[string]any, len(text.ProviderMetadata)+1)
		maps.Copy(metadata, text.ProviderMetadata)
		metadata[event.ContextInjectionMetadataKey] = kind
		text.ProviderMetadata = metadata
		content := append([]sdk.MessagePart(nil), message.Content...)
		content[i] = text
		message.Content = content
		return message
	}
	return message
}
