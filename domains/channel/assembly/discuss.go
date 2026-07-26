package assembly

import (
	"context"
	"log/slog"

	agentdomain "github.com/memohai/memoh/domains/agent"
	messagepkg "github.com/memohai/memoh/domains/agent/chat/message"
	"github.com/memohai/memoh/domains/agent/chat/timeline"
	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/inbound"
	"github.com/memohai/memoh/domains/channel/internal/discuss"
)

// DiscussCursorStore persists the last timeline position consumed by a
// discuss-mode worker.
type DiscussCursorStore interface {
	GetDiscussCursor(context.Context, string, string) (timeline.DiscussCursorPosition, error)
	UpsertDiscussCursor(context.Context, string, string, string, string, string, timeline.DiscussCursorPosition) error
}

// DiscussArtifactProvider supplies the active compaction frontier used to
// compose discuss-mode context.
type DiscussArtifactProvider interface {
	ActiveCompactionArtifacts(context.Context, string, string) ([]timeline.CompactionArtifact, error)
}

// DiscussStreamBroadcaster publishes discuss-mode Agent events to local stream
// subscribers.
type DiscussStreamBroadcaster interface {
	PublishEvent(string, gateway.StreamEvent)
}

// NewDiscussDriver wires the private Channel implementation to its public
// inbound contract. All runtime dependencies are fixed at construction.
func NewDiscussDriver(
	log *slog.Logger,
	turns agentdomain.Service,
	messages messagepkg.Service,
	cursors DiscussCursorStore,
	artifacts DiscussArtifactProvider,
	broadcaster DiscussStreamBroadcaster,
) inbound.DiscussDriver {
	return discuss.NewDiscussDriver(discuss.DiscussDriverDeps{
		Turn:           turns,
		MessageService: messages,
		CursorStore:    cursors,
		Artifacts:      artifacts,
		Broadcaster:    broadcaster,
		Logger:         log,
	})
}
