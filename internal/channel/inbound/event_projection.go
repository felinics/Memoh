package inbound

import (
	"context"
	"log/slog"

	"github.com/felinics/memoh/internal/chat/timeline"
)

// sessionEventPersister is the narrow slice of the timeline event store this
// package needs to durably stamp an inbound event.
type sessionEventPersister interface {
	PersistEvent(ctx context.Context, botID, sessionID string, event timeline.CanonicalEvent) (string, timeline.CanonicalEvent, error)
}

// persistAndProjectEvent projects the persisted form of the event, not the
// adapted input: the store stamps the durable event cursor that discuss
// consumption gates on, so projecting the pre-persist value would silently
// degrade every segment to source-time gating.
func persistAndProjectEvent(
	ctx context.Context,
	store sessionEventPersister,
	pipeline *timeline.Pipeline,
	log *slog.Logger,
	botID string,
	sessionID string,
	event timeline.CanonicalEvent,
) (string, timeline.RenderedContext) {
	eventID := ""
	if store != nil {
		id, persisted, err := store.PersistEvent(ctx, botID, sessionID, event)
		event = persisted
		if err != nil {
			if log != nil {
				log.Warn("persist pipeline event failed", slog.Any("error", err))
			}
		} else {
			eventID = id
		}
	}
	return eventID, pipeline.PushEvent(sessionID, event)
}
