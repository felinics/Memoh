package message

import (
	"context"
	"time"
)

// ObservedRouteQuery selects routes with visible messages for one bot. An empty
// ChannelIdentityID includes visible messages from every sender.
type ObservedRouteQuery struct {
	BotID             string
	ChannelIdentityID string
}

// ObservedRoute is the Agent-owned observation projected for Channel
// enrichment. Route metadata remains Channel-owned.
type ObservedRoute struct {
	RouteID        string
	LastObservedAt time.Time
}

type ObservedRouteReader interface {
	ListObservedRoutes(context.Context, ObservedRouteQuery) ([]ObservedRoute, error)
}
