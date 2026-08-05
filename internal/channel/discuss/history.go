package discuss

import (
	"context"
	"log/slog"
	"time"

	messagepkg "github.com/memohai/memoh/internal/chat/message"
	"github.com/memohai/memoh/internal/chat/timeline"
)

type discussHistoryReader struct {
	messages messagepkg.Service
	logger   *slog.Logger
}

// Load reads every persisted assistant/tool response for symmetric timeline
// composition. Compaction, not this adapter, owns size trimming.
func (r discussHistoryReader) Load(ctx context.Context, sessionID string) []timeline.TurnResponseEntry {
	if r.messages == nil {
		return nil
	}
	messages, err := r.messages.ListActiveSinceBySession(ctx, sessionID, time.Unix(0, 0).UTC())
	if err != nil {
		r.logger.Warn("load TRs failed", slog.String("session_id", sessionID), slog.Any("error", err))
		return nil
	}

	return timeline.DecodeTurnResponseEntries(messages)
}
