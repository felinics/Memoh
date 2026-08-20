package native

import sdk "github.com/memohai/twilight-ai/sdk"

// ContextProjection is the native runtime's provider-facing context input.
// Context selection owns the semantic fragments; Native owns this SDK-shaped
// wire projection and sends it through the Twilight provider loop.
type ContextProjection struct {
	System       string
	Messages     []sdk.Message
	Query        string
	InlineImages []sdk.ImagePart
}

// Clone returns an independent projection so a runtime adapter can append
// provider parts without mutating the ContextView renderer's snapshot.
func (p ContextProjection) Clone() ContextProjection {
	clone := p
	clone.Messages = cloneSDKMessages(p.Messages)
	clone.InlineImages = append([]sdk.ImagePart(nil), p.InlineImages...)
	return clone
}

func cloneSDKMessages(messages []sdk.Message) []sdk.Message {
	if len(messages) == 0 {
		return nil
	}
	clone := make([]sdk.Message, len(messages))
	for i, message := range messages {
		clone[i] = message
		clone[i].Content = append([]sdk.MessagePart(nil), message.Content...)
	}
	return clone
}
