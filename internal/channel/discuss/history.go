package discuss

import (
	"context"
	"log/slog"
	"time"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/timeline"
)

// HistoryReader is the lightweight history surface needed by Discuss. Keeping
// it separate from the general message service prevents loading UI/audit data.
type HistoryReader interface {
	MeasureActiveBySession(context.Context, string, time.Time) (messagepkg.ActiveMessagesMeasure, error)
	ListDiscussHistorySinceBySessionWithinBytes(context.Context, string, time.Time, int64) ([]messagepkg.Message, error)
}

type discussHistoryReader struct {
	messages HistoryReader
	// maxBytes selects the newest content window (CM-ADM-001). The query
	// retains the crossing row and only projects the interrupted flag from
	// metadata, so legacy lifecycle audits stay outside the loaded window.
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
		r.logger.WarnContext(ctx, "measure TRs failed", slog.String("session_id", sessionID), slog.Any("error", err))
	}
	messages, err := r.messages.ListDiscussHistorySinceBySessionWithinBytes(ctx, sessionID, since, r.maxBytes)
	if err != nil {
		r.logger.WarnContext(ctx, "load TRs failed", slog.String("session_id", sessionID), slog.Any("error", err))
		return nil, measure
	}
	measure.Loaded = len(messages)
	return timeline.DecodeTurnResponseEntries(messages), measure
}
