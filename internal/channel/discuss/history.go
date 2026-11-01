package discuss

import (
	"context"
	"log/slog"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/timeline"
)

// HistoryReader is the lightweight history surface needed by Discuss. Keeping
// it separate from the general message service prevents loading UI/audit data.
type HistoryReader interface {
	ListTurnResponseSourcesWithinBytes(context.Context, string, messagepkg.HistoryPosition, int64) (messagepkg.HistoryLoad, error)
}

// DiscussCheckpointSource reads a session's active history checkpoint for
// composing its rendered context. *timeline.CheckpointSource implements it.
type DiscussCheckpointSource interface {
	Load(ctx context.Context, sessionID string, rc timeline.RenderedContext) (timeline.HistoryCheckpoint, error)
}

type discussHistoryReader struct {
	messages HistoryReader
	// maxBytes selects the newest content window (CM-ADM-001). The query
	// retains the crossing row and only projects the interrupted flag from
	// metadata, so legacy lifecycle audits stay outside the loaded window.
	maxBytes int64
	logger   *slog.Logger
}

// Load reads the byte-budgeted recent window of persisted assistant/tool
// responses after the session's history checkpoint for timeline
// composition, and whether the budget left older responses out. The
// checkpoint stands for the ones before it.
func (r discussHistoryReader) Load(ctx context.Context, sessionID string, after messagepkg.HistoryPosition) ([]timeline.TurnResponseEntry, bool, error) {
	if r.messages == nil {
		return nil, false, nil
	}
	load, err := r.messages.ListTurnResponseSourcesWithinBytes(ctx, sessionID, after, r.maxBytes)
	if err != nil {
		return nil, false, err
	}
	return timeline.DecodeTurnResponseEntries(load.Messages), load.Truncated, nil
}
