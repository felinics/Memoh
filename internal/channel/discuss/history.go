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
	HistoryOriginBySession(context.Context, string) (messagepkg.HistoryOrigin, error)
	MeasureActiveBySession(context.Context, string, time.Time) (messagepkg.ActiveMessagesMeasure, error)
	ListTurnResponseSourcesSinceBySessionWithinBytes(context.Context, string, time.Time, int64) ([]messagepkg.Message, error)
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

// Origin reports where the session's context starts.
func (r discussHistoryReader) Origin(ctx context.Context, sessionID string) (messagepkg.HistoryOrigin, error) {
	if r.messages == nil {
		return messagepkg.HistoryOrigin{}, nil
	}
	return r.messages.HistoryOriginBySession(ctx, sessionID)
}

// Load reads the byte-budgeted recent window of persisted assistant/tool
// responses from the session's history origin on for timeline composition,
// measuring the full extent with a metadata-only aggregate first. Older
// responses beyond the budget stay in the database; compaction artifacts
// represent them in composition.
func (r discussHistoryReader) Load(ctx context.Context, sessionID string, origin messagepkg.HistoryOrigin) ([]timeline.TurnResponseEntry, discussHistoryMeasure, error) {
	if r.messages == nil {
		return nil, discussHistoryMeasure{}, nil
	}
	since := time.Unix(0, 0).UTC()
	if !origin.At.IsZero() {
		since = origin.At
	}
	measure := discussHistoryMeasure{}
	if agg, err := r.messages.MeasureActiveBySession(ctx, sessionID, since); err == nil {
		measure.TotalMessages = agg.MessageCount
		measure.TotalBytes = agg.ContentBytes
	} else {
		r.logger.WarnContext(ctx, "measure TRs failed", slog.String("session_id", sessionID), slog.Any("error", err))
	}
	messages, err := r.messages.ListTurnResponseSourcesSinceBySessionWithinBytes(ctx, sessionID, since, r.maxBytes)
	if err != nil {
		return nil, measure, err
	}
	measure.Loaded = len(messages)
	return timeline.DecodeTurnResponseEntries(messages), measure, nil
}
