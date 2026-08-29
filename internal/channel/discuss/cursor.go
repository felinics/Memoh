package discuss

import (
	"context"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/chat/timeline"
)

type discussCursorTracker struct {
	store DiscussCursorStore
}

func (t discussCursorTracker) Load(ctx context.Context, cfg DiscussSessionConfig, log *slog.Logger) timeline.DiscussCursorPosition {
	if t.store == nil {
		return timeline.DiscussCursorPosition{}
	}
	position, err := t.store.GetDiscussCursor(ctx, cfg.ThreadID, discussCursorScope(cfg))
	if err != nil {
		log.Warn("discuss cursor load failed", slog.Any("error", err))
		return timeline.DiscussCursorPosition{}
	}
	return position
}

func (t discussCursorTracker) Advance(ctx context.Context, sess *discussSession, cfg DiscussSessionConfig, position timeline.DiscussCursorPosition, log *slog.Logger) {
	merged := sess.lastProcessed.Merge(position)
	if merged == sess.lastProcessed {
		return
	}
	sess.lastProcessed = merged
	if t.store == nil {
		return
	}
	if err := t.store.UpsertDiscussCursor(
		ctx,
		cfg.ThreadID,
		discussCursorScope(cfg),
		strings.TrimSpace(cfg.RouteID),
		strings.TrimSpace(cfg.CurrentPlatform),
		merged,
	); err != nil {
		log.Warn("discuss cursor persist failed", slog.Any("error", err), slog.Int64("cursor", merged.EventCursor), slog.Int64("source_cursor", merged.SourceCursor))
	}
}

func discussCursorScope(cfg DiscussSessionConfig) string {
	if routeID := strings.TrimSpace(cfg.RouteID); routeID != "" {
		return "route:" + routeID
	}
	platform := strings.TrimSpace(cfg.CurrentPlatform)
	identityID := strings.TrimSpace(cfg.ChannelIdentityID)
	switch {
	case platform != "" && identityID != "":
		return "source:" + platform + ":" + identityID
	case platform != "":
		return "source:" + platform
	default:
		return "default"
	}
}

// anchorFromTRs returns the latest persisted turn request timestamp used to
// avoid replaying old external context after a worker restart.
func anchorFromTRs(trs []timeline.TurnResponseEntry) int64 {
	var latest int64
	for _, response := range trs {
		if response.RequestedAtMs > latest {
			latest = response.RequestedAtMs
		}
	}
	return latest
}
