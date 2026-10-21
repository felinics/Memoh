package discuss

import (
	"context"
	"log/slog"
	"time"

	"github.com/felinics/memoh/internal/chat/contextepoch"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/timeline"
)

// HistoryReader is the lightweight history surface needed by Discuss. Keeping
// it separate from the general message service prevents loading UI/audit data.
type HistoryReader interface {
	MeasureActiveBySession(context.Context, string, time.Time) (messagepkg.ActiveMessagesMeasure, error)
	ListTurnResponseSourcesSinceBySessionWithinBytes(context.Context, string, time.Time, int64) ([]messagepkg.Message, error)
}

type discussHistoryReader struct {
	messages HistoryReader
	// epochs anchors the load at the session's persisted Context Epoch state
	// (docs/design/context-epoch-spec.md, rule 2). Nil keeps the constant
	// beginning-of-session anchor, which is what the zero state resolves to.
	epochs *contextepoch.Store
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

// Load reads the byte-budgeted window of persisted assistant/tool responses
// for timeline composition, starting from the session's Context Epoch anchor,
// and measures the anchored extent with a metadata-only aggregate for
// admission observability (CM-ADM-001, CM-OBS-001). A budget-bound load
// records an explicit anchor advance (reason "budget") through the epoch
// store instead of silently sliding. Older responses beyond the budget stay
// in the database; compaction artifacts represent them in composition.
func (r discussHistoryReader) Load(ctx context.Context, sessionID string) ([]timeline.TurnResponseEntry, discussHistoryMeasure) {
	measure := discussHistoryMeasure{}
	if r.messages == nil {
		return nil, measure
	}
	measureFn := func(ctx context.Context, since time.Time) (int64, error) {
		agg, err := r.messages.MeasureActiveBySession(ctx, sessionID, since)
		if err != nil {
			return 0, err
		}
		measure.TotalMessages = agg.MessageCount
		measure.TotalBytes = agg.ContentBytes
		return agg.MessageCount, nil
	}
	loadFn := func(ctx context.Context, since time.Time) ([]messagepkg.Message, error) {
		return r.messages.ListTurnResponseSourcesSinceBySessionWithinBytes(ctx, sessionID, since, r.maxBytes)
	}
	var messages []messagepkg.Message
	var err error
	if r.epochs != nil {
		messages, err = r.epochs.LoadWindow(ctx, sessionID, "discuss", measureFn, loadFn)
	} else {
		since := contextepoch.Beginning
		if _, mErr := measureFn(ctx, since); mErr != nil {
			r.logger.WarnContext(ctx, "measure TRs failed", slog.String("session_id", sessionID), slog.Any("error", mErr))
		}
		messages, err = loadFn(ctx, since)
	}
	if err != nil {
		r.logger.WarnContext(ctx, "load TRs failed", slog.String("session_id", sessionID), slog.Any("error", err))
		return nil, measure
	}
	measure.Loaded = len(messages)
	return timeline.DecodeTurnResponseEntries(messages), measure
}
