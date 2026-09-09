package discuss

import (
	"context"
	"log/slog"
	"time"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/timeline"
)

type discussHistoryReader struct {
	messages messagepkg.Service
	// maxBytes bounds every history load (CM-ADM-001): rows are admitted
	// newest-first on the database side until the content byte budget is
	// spent, so total history size never bounds process memory.
	maxBytes int64
	logger   *slog.Logger
}

// discussHistoryMeasure reports the database-side aggregate next to what was
// actually loaded, for admission observability (CM-OBS-001).
type discussHistoryMeasure struct {
	TotalMessages int64
	TotalBytes    int64
	Loaded        int
}

// Load reads the byte-budgeted recent window of persisted assistant/tool
// responses for timeline composition, measuring the full extent with a
// metadata-only aggregate first. Older responses beyond the budget stay in
// the database; compaction artifacts represent them in composition.
func (r discussHistoryReader) Load(ctx context.Context, sessionID string) ([]timeline.TurnResponseEntry, discussHistoryMeasure) {
	if r.messages == nil {
		return nil, discussHistoryMeasure{}
	}
	since := time.Unix(0, 0).UTC()
	measure := discussHistoryMeasure{}
	if agg, err := r.messages.MeasureActiveBySession(ctx, sessionID, since); err == nil {
		measure.TotalMessages = agg.MessageCount
		measure.TotalBytes = agg.ContentBytes
	} else {
		r.logger.Warn("measure TRs failed", slog.String("session_id", sessionID), slog.Any("error", err))
	}
	messages, err := r.messages.ListActiveSinceBySessionWithinBytes(ctx, sessionID, since, r.maxBytes)
	if err != nil {
		r.logger.Warn("load TRs failed", slog.String("session_id", sessionID), slog.Any("error", err))
		return nil, measure
	}
	measure.Loaded = len(messages)
	return timeline.DecodeTurnResponseEntries(messages), measure
}
