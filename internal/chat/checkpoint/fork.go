package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// Copy is the history a fork copied from its source session, under the
// source's share lock (LockSessionForFork).
type Copy struct {
	SourceSessionID string
	ForkSessionID   string
	// SourceMessageIDs and ForkMessageIDs pair each copied source row with
	// its copy.
	SourceMessageIDs []pgtype.UUID
	ForkMessageIDs   []pgtype.UUID
	// ThroughTurnPosition is the source position of the last turn the fork
	// copied.
	ThroughTurnPosition int64
}

// CopyToFork gives a fork the newest checkpoint on its source's active chain
// that accounts for no history past the copy, in the transaction q runs in:
// its summaries become the fork's own, naming the copies of the rows they
// cite (renameRows), the rows it keeps are named by their copies, and its
// position becomes the copy's place for it. The fork's checkpoint has no
// parent: nothing before it exists in the fork. The fork copied every visible
// row through the turn, so it copied every row such a checkpoint keeps or its
// summaries cite.
func CopyToFork(ctx context.Context, q dbstore.Queries, c Copy) error {
	sourceID, err := dbpkg.ParseUUID(c.SourceSessionID)
	if err != nil {
		return err
	}
	forkID, err := dbpkg.ParseUUID(c.ForkSessionID)
	if err != nil {
		return err
	}
	row, err := q.GetHistoryCheckpointKeptThroughTurn(ctx, sqlc.GetHistoryCheckpointKeptThroughTurnParams{
		SessionID:           sourceID,
		ThroughTurnPosition: c.ThroughTurnPosition,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load source history checkpoint: %w", err)
	}
	source, err := resolve(ctx, q, row)
	if err != nil {
		return err
	}
	copies := make(map[string]pgtype.UUID, len(c.SourceMessageIDs))
	for i, id := range c.SourceMessageIDs {
		copies[id.String()] = c.ForkMessageIDs[i]
	}
	items := make([]Item, 0, len(source.Items))
	for _, item := range source.Items {
		if item.RowID != "" {
			copied, ok := copies[item.RowID]
			if !ok {
				return fmt.Errorf("history checkpoint %s keeps row %s, which the fork did not copy", source.ID, item.RowID)
			}
			items = append(items, Item{RowID: copied.String()})
			continue
		}
		summaryID, err := dbpkg.ParseUUID(item.SummaryID)
		if err != nil {
			return err
		}
		summary := renameRows(item.Summary, copies)
		copied, err := q.CopyCompactionSummary(ctx, sqlc.CopyCompactionSummaryParams{Summary: summary, SessionID: forkID, SourceCompactID: summaryID})
		if err != nil {
			return fmt.Errorf("copy summary %s: %w", item.SummaryID, err)
		}
		items = append(items, Item{SummaryID: copied.String(), Summary: summary})
	}
	position, err := q.GetForkHistoryPosition(ctx, sqlc.GetForkHistoryPositionParams{
		TurnPosition:     source.Position.Turn,
		TurnMessageSeq:   source.Position.Seq,
		SourceMessageIds: c.SourceMessageIDs,
		ForkMessageIds:   c.ForkMessageIDs,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("map history checkpoint position: %w", err)
	}
	_, err = create(ctx, q, forkID, Checkpoint{}, messagepkg.HistoryPosition{Turn: position.TurnPosition, Seq: position.TurnMessageSeq}, items)
	return err
}

// rowID matches the id of a row in a summary's text.
var rowID = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// renameRows is a summary with each row it names that the fork copied named
// by the copy. A summary cites the tool results it folds by their rows
// (ref=<row>:<part>), and get_messages reads a ref back only from the session
// that holds the row.
func renameRows(summary string, copies map[string]pgtype.UUID) string {
	return rowID.ReplaceAllStringFunc(summary, func(id string) string {
		if copied, ok := copies[strings.ToLower(id)]; ok {
			return copied.String()
		}
		return id
	})
}
