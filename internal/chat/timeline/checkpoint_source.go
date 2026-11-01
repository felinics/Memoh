package timeline

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/chat/checkpoint"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// HistoryCheckpoint is the timeline-facing projection of a session's active
// history checkpoint, read for the segments a composition renders. The zero
// value folds nothing.
type HistoryCheckpoint struct {
	// Position is where the turn responses composed after it start.
	Position messagepkg.HistoryPosition
	// Items are what the checkpoint holds, in its order.
	Items []CheckpointItem
	// Messages tells, for each inbound message of the composed segments that
	// has a context row, how the checkpoint stands for it.
	Messages map[string]CheckpointMessage
}

// CheckpointMessage is how a history checkpoint stands for an inbound
// message with context rows.
type CheckpointMessage struct {
	// Kept reports whether the checkpoint leaves one of its rows raw.
	Kept bool
}

// CheckpointItem is one item of a HistoryCheckpoint: a summary or a row it
// keeps raw.
type CheckpointItem struct {
	SummaryID string
	Summary   string
	// MessageID names the inbound message a kept user row records: the
	// segment of that message renders for it.
	MessageID string
	// Response is what a kept assistant or tool row renders as.
	Response *TurnResponseEntry
}

// composes reports whether a segment the checkpoint does not hold composes
// after it: one whose message has no context rows, or one whose rows the
// checkpoint leaves raw. A summary stands for a message it folded as its rows
// recorded it, so an edit or delete after the fold does not bring it back.
func (c HistoryCheckpoint) composes(segment RenderedSegment) bool {
	message, ok := c.Messages[strings.TrimSpace(segment.MessageID)]
	return !ok || message.Kept
}

// CheckpointRows reads the rows a history checkpoint keeps by reference.
type CheckpointRows interface {
	ListTurnResponseSourcesByIDs(ctx context.Context, sessionID string, ids []string) ([]messagepkg.Message, error)
}

// CheckpointSource reads a session's active history checkpoint as
// composition renders it.
type CheckpointSource struct {
	checkpoints *checkpoint.Store
	queries     dbstore.Queries
	rows        CheckpointRows
}

// NewCheckpointSource creates a CheckpointSource.
func NewCheckpointSource(log *slog.Logger, queries dbstore.Queries, rows CheckpointRows) *CheckpointSource {
	return &CheckpointSource{checkpoints: checkpoint.NewStore(log, queries), queries: queries, rows: rows}
}

// Load reads the session's active checkpoint for composing rc: what it
// holds, the rows it keeps as they render, and how it stands for the inbound
// messages of rc. A kept user row that records no inbound message renders
// nothing, as no such row reaches composition otherwise.
func (s *CheckpointSource) Load(ctx context.Context, sessionID string, rc RenderedContext) (HistoryCheckpoint, error) {
	active, err := s.checkpoints.Active(ctx, sessionID)
	if err != nil || active.ID == "" {
		return HistoryCheckpoint{}, err
	}
	projection := HistoryCheckpoint{Position: active.Position}
	kept := make(map[string]messagepkg.Message)
	if rows := active.RowIDs(); len(rows) > 0 {
		messages, err := s.rows.ListTurnResponseSourcesByIDs(ctx, sessionID, rows)
		if err != nil {
			return HistoryCheckpoint{}, fmt.Errorf("load history checkpoint rows: %w", err)
		}
		for _, message := range messages {
			kept[message.ID] = message
		}
	}
	for _, item := range active.Items {
		if item.SummaryID != "" {
			projection.Items = append(projection.Items, CheckpointItem{SummaryID: item.SummaryID, Summary: item.Summary})
			continue
		}
		message, ok := kept[item.RowID]
		if !ok {
			return HistoryCheckpoint{}, fmt.Errorf("history checkpoint %s keeps row %s, which is not a context row", active.ID, item.RowID)
		}
		if strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			if message.ExternalMessageID != "" {
				projection.Items = append(projection.Items, CheckpointItem{MessageID: message.ExternalMessageID})
			}
			continue
		}
		if response, ok := DecodeTurnResponseEntry(message); ok {
			projection.Items = append(projection.Items, CheckpointItem{Response: &response})
		}
	}
	externalIDs := make([]string, 0, len(rc))
	for _, segment := range rc {
		if id := strings.TrimSpace(segment.MessageID); id != "" {
			externalIDs = append(externalIDs, id)
		}
	}
	if len(externalIDs) == 0 {
		return projection, nil
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return HistoryCheckpoint{}, err
	}
	classified, err := s.queries.ClassifyHistoryCheckpointMessages(ctx, sqlc.ClassifyHistoryCheckpointMessagesParams{
		AfterTurnPosition:   active.Position.Turn,
		AfterTurnMessageSeq: active.Position.Seq,
		KeptIds:             active.RowUUIDs(),
		SessionID:           pgSessionID,
		ExternalMessageIds:  externalIDs,
	})
	if err != nil {
		return HistoryCheckpoint{}, fmt.Errorf("classify history checkpoint messages: %w", err)
	}
	projection.Messages = make(map[string]CheckpointMessage, len(classified))
	for _, row := range classified {
		projection.Messages[row.ExternalMessageID] = CheckpointMessage{Kept: row.Kept}
	}
	return projection, nil
}
