package discuss

import (
	"context"
	"log/slog"
	"strings"

	"github.com/memohai/memoh/domains/agent/chat/timeline"
)

type discussCursorTracker struct {
	store DiscussCursorStore
}

func (t discussCursorTracker) Load(ctx context.Context, cfg DiscussSessionConfig, log *slog.Logger) int64 {
	if t.store == nil {
		return 0
	}
	cursor, err := t.store.GetDiscussConsumedCursor(ctx, cfg.ThreadID, discussCursorScope(cfg))
	if err != nil {
		log.WarnContext(ctx, "discuss cursor load failed", slog.Any("error", err))
		return 0
	}
	return cursor
}

func (t discussCursorTracker) Advance(ctx context.Context, sess *discussSession, cfg DiscussSessionConfig, cursor int64, log *slog.Logger) {
	if cursor <= sess.lastProcessedMs {
		return
	}
	sess.lastProcessedMs = cursor
	if t.store == nil {
		return
	}
	if err := t.store.UpsertDiscussConsumedCursor(
		ctx,
		cfg.BotID,
		cfg.ThreadID,
		discussCursorScope(cfg),
		strings.TrimSpace(cfg.RouteID),
		strings.TrimSpace(cfg.CurrentPlatform),
		cursor,
	); err != nil {
		log.WarnContext(ctx, "discuss cursor persist failed", slog.Any("error", err), slog.Int64("cursor", cursor))
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

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
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
