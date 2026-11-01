package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// ErrMoved reports a fold whose parent is no longer the session's active
// checkpoint: the history moved on since the fold read it.
var ErrMoved = errors.New("history checkpoint moved since it was read")

// Row is a context row at its place in the history.
type Row struct {
	ID       string
	Position messagepkg.HistoryPosition
}

// Fold is a summary a compaction pass commits onto the checkpoint it read.
type Fold struct {
	SessionID string
	// Parent is the checkpoint the pass folded onto: the session's active one
	// when it read its candidates.
	Parent Checkpoint
	// SummaryID names the pass's compaction log and Summary is its text.
	SummaryID string
	Summary   string
	// Folded are the rows the summary folds.
	Folded []Row
	// Read names every row the pass read, folded or not.
	Read []string
}

// Commit makes the checkpoint of a fold the session's active one, in the
// transaction q runs in, the run's fenced one. The checkpoint
// accounts for the history through the last row it folds, or through its
// parent's position when it folds none. It keeps every context row there it
// does not fold, the rows its parent kept included, each in history order:
// the ones the pass read before its summary, which absorbs its parent's, as
// the request that read them places the summary after them, and the ones it
// never read after it, since the summary was written without them. Those are
// logged and counted.
func (s *Store) Commit(ctx context.Context, q dbstore.Queries, fold Fold) (Checkpoint, error) {
	pgSessionID, err := dbpkg.ParseUUID(fold.SessionID)
	if err != nil {
		return Checkpoint{}, err
	}
	through := fold.Parent.Position
	folded := make(map[string]bool, len(fold.Folded))
	ids := make([]pgtype.UUID, 0, len(fold.Folded)+len(fold.Parent.Items))
	for _, row := range fold.Folded {
		if through.Before(row.Position) {
			through = row.Position
		}
		folded[row.ID] = true
		id, err := dbpkg.ParseUUID(row.ID)
		if err != nil {
			return Checkpoint{}, err
		}
		ids = append(ids, id)
	}
	for _, row := range fold.Parent.RowIDs() {
		id, err := dbpkg.ParseUUID(row)
		if err != nil {
			return Checkpoint{}, err
		}
		ids = append(ids, id)
	}
	span, err := q.ListHistoryCheckpointSpanRows(ctx, sqlc.ListHistoryCheckpointSpanRowsParams{
		SessionID:             pgSessionID,
		AfterTurnPosition:     fold.Parent.Position.Turn,
		AfterTurnMessageSeq:   fold.Parent.Position.Seq,
		ThroughTurnPosition:   through.Turn,
		ThroughTurnMessageSeq: through.Seq,
		Ids:                   ids,
	})
	if err != nil {
		return Checkpoint{}, fmt.Errorf("load history checkpoint span: %w", err)
	}
	read := make(map[string]bool, len(fold.Read))
	for _, id := range fold.Read {
		read[id] = true
	}
	items := make([]Item, 0, len(span)+1)
	var unseen []Item
	for _, row := range span {
		id := row.ID.String()
		switch {
		case folded[id]:
		case read[id]:
			items = append(items, Item{RowID: id})
		default:
			unseen = append(unseen, Item{RowID: id})
		}
	}
	items = append(items, Item{SummaryID: fold.SummaryID, Summary: fold.Summary})
	items = append(items, unseen...)
	created, err := create(ctx, q, pgSessionID, fold.Parent, through, items)
	if err != nil {
		return Checkpoint{}, err
	}
	if len(unseen) > 0 {
		s.logger.WarnContext(ctx, "history checkpoint keeps rows its summary never read",
			slog.String("session_id", fold.SessionID),
			slog.String("history_checkpoint_id", created.ID),
			slog.Int("unseen_rows", len(unseen)))
		countUnseenRows(ctx, len(unseen))
	}
	return created, nil
}

// create writes a checkpoint after parent and makes it the session's active
// one in place of parent, while parent is still the active one.
func create(ctx context.Context, q dbstore.Queries, sessionID pgtype.UUID, parent Checkpoint, position messagepkg.HistoryPosition, items []Item) (Checkpoint, error) {
	encoded, err := json.Marshal(items)
	if err != nil {
		return Checkpoint{}, err
	}
	params := sqlc.CreateHistoryCheckpointParams{
		ParentID:       dbpkg.ParseUUIDOrEmpty(parent.ID),
		TurnPosition:   position.Turn,
		TurnMessageSeq: position.Seq,
		Items:          encoded,
		SessionID:      sessionID,
	}
	row, err := q.CreateHistoryCheckpoint(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return Checkpoint{}, fmt.Errorf("%w: session %s", ErrMoved, sessionID.String())
	}
	if err != nil {
		return Checkpoint{}, fmt.Errorf("create history checkpoint: %w", err)
	}
	moved, err := q.SetHistoryCheckpoint(ctx, sqlc.SetHistoryCheckpointParams{
		CheckpointID:         row.ID,
		SessionID:            sessionID,
		ExpectedCheckpointID: params.ParentID,
	})
	if err != nil {
		return Checkpoint{}, fmt.Errorf("activate history checkpoint: %w", err)
	}
	if moved != 1 {
		return Checkpoint{}, fmt.Errorf("%w: session %s", ErrMoved, sessionID.String())
	}
	created, err := fromRow(row)
	if err != nil {
		return Checkpoint{}, err
	}
	created.Items = items
	return created, nil
}
