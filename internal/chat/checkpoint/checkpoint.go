// Package checkpoint keeps the history checkpoints of a session: what stands
// in a request for the history at or before a position, so that only the
// rows after it load raw.
//
// A checkpoint holds, in order, the rows it keeps by reference and the
// summaries that fold the rest; it always holds a summary. A request renders
// the items in their order, and then the session's context rows after the
// checkpoint's position: its visible rows of complete turns, other than
// legacy passive_sync rows. Compaction folds onto the active checkpoint and
// makes the result active; a fork takes a copy of its source's. Every
// checkpoint names its parent: hiding, replacing or deleting rows at or
// before the active one's position moves the session back, in the same
// statement, to the newest one on its chain before them.
package checkpoint

import (
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// Item is one entry of a checkpoint: a row it keeps by reference or a
// summary, named by its compaction log.
type Item struct {
	RowID     string `json:"row,omitempty"`
	SummaryID string `json:"summary,omitempty"`
	// Summary is the summary's text, filled in when the checkpoint is read
	// for a request.
	Summary string `json:"-"`
}

// Checkpoint is one history checkpoint of a session. The zero Checkpoint
// stands for nothing: every context row of the session renders raw.
type Checkpoint struct {
	ID       string
	ParentID string
	// Position is the last place in the history the checkpoint accounts for.
	Position messagepkg.HistoryPosition
	Items    []Item
}

// RowIDs names the rows the checkpoint keeps by reference, in item order.
func (c Checkpoint) RowIDs() []string {
	var rows []string
	for _, item := range c.Items {
		if item.RowID != "" {
			rows = append(rows, item.RowID)
		}
	}
	return rows
}

// RowUUIDs names the rows the checkpoint keeps by reference as the queries
// that take them do.
func (c Checkpoint) RowUUIDs() []pgtype.UUID {
	rows := c.RowIDs()
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, id := range rows {
		ids = append(ids, dbpkg.ParseUUIDOrEmpty(id))
	}
	return ids
}

// SummaryIDs names the summaries the checkpoint holds, in item order.
func (c Checkpoint) SummaryIDs() []string {
	var summaries []string
	for _, item := range c.Items {
		if item.SummaryID != "" {
			summaries = append(summaries, item.SummaryID)
		}
	}
	return summaries
}

func fromRow(row sqlc.BotHistoryCheckpoint) (Checkpoint, error) {
	var items []Item
	if err := json.Unmarshal(row.Items, &items); err != nil {
		return Checkpoint{}, fmt.Errorf("decode items of history checkpoint %s: %w", row.ID, err)
	}
	for i, item := range items {
		if (item.RowID == "") == (item.SummaryID == "") {
			return Checkpoint{}, fmt.Errorf("history checkpoint %s: item %d names neither one row nor one summary", row.ID, i)
		}
	}
	return Checkpoint{
		ID:       row.ID.String(),
		ParentID: row.ParentID.String(),
		Position: messagepkg.HistoryPosition{Turn: row.TurnPosition, Seq: row.TurnMessageSeq},
		Items:    items,
	}, nil
}
