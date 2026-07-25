package channel

import "context"

// ConversationProjectionRequest selects Channel-owned route projections.
// ChannelType is optional; when set, only matching route types are returned.
type ConversationProjectionRequest struct {
	BotID       string
	RouteIDs    []string
	ChannelType string
}

// ConversationProjection is the Channel-owned display projection for a route.
type ConversationProjection struct {
	RouteID               string
	Channel               string
	ConversationType      string
	ConversationID        string
	ThreadID              string
	ConversationName      string
	ConversationAvatarURL string
}

type ConversationProjectionReader interface {
	ListConversationProjections(context.Context, ConversationProjectionRequest) ([]ConversationProjection, error)
}
