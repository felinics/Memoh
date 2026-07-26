package inbound

import (
	"context"

	"github.com/memohai/memoh/domains/agent/chat/timeline"
)

// DiscussDriver receives rendered context updates for discuss-mode sessions
// and owns the lifetime of their background workers.
type DiscussDriver interface {
	NotifyRC(context.Context, string, timeline.RenderedContext, DiscussSessionConfig)
	Shutdown(context.Context) error
}

// DiscussSessionConfig is the routing and authorization context needed to run
// a discuss-mode turn for one thread.
type DiscussSessionConfig struct {
	TeamID            string
	BotID             string
	ThreadID          string
	RouteID           string
	ChannelIdentityID string
	ReplyTarget       string
	CurrentPlatform   string
	ConversationType  string
	ConversationName  string
	SessionToken      string //nolint:gosec // session credential material
	ChatToken         string //nolint:gosec // scoped chat routing token
	ToolHTTPURL       string
}
