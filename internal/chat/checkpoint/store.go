package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const meterName = "github.com/felinics/memoh/internal/chat/checkpoint"

// Store reads and writes the history checkpoints of sessions.
type Store struct {
	queries dbstore.Queries
	logger  *slog.Logger
}

// NewStore creates a Store.
func NewStore(log *slog.Logger, queries dbstore.Queries) *Store {
	if log == nil {
		log = slog.Default()
	}
	return &Store{queries: queries, logger: log.With(slog.String("service", "chat/checkpoint"))}
}

// Active is the session's active checkpoint as a request renders it, with
// the text of its summaries: the zero Checkpoint when the session has none.
func (s *Store) Active(ctx context.Context, sessionID string) (Checkpoint, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return Checkpoint{}, err
	}
	row, err := s.queries.GetActiveHistoryCheckpoint(ctx, pgSessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Checkpoint{}, nil
	}
	if err != nil {
		return Checkpoint{}, fmt.Errorf("load active history checkpoint: %w", err)
	}
	return resolve(ctx, s.queries, row)
}

// KeptBeforeTurn is the checkpoint the session keeps once the turn's context
// rows are replaced, without making it active: what an edit or retry renders
// before its replacement commits.
func (s *Store) KeptBeforeTurn(ctx context.Context, sessionID, turnID string) (Checkpoint, error) {
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		return Checkpoint{}, err
	}
	pgTurnID, err := dbpkg.ParseUUID(turnID)
	if err != nil {
		return Checkpoint{}, err
	}
	kept, err := s.queries.GetHistoryCheckpointKeptBeforeTurn(ctx, sqlc.GetHistoryCheckpointKeptBeforeTurnParams{SessionID: pgSessionID, TurnID: pgTurnID})
	if err != nil {
		return Checkpoint{}, fmt.Errorf("load history checkpoint kept before turn: %w", err)
	}
	if !kept.TurnFound {
		return Checkpoint{}, fmt.Errorf("load history checkpoint kept before turn: turn %s has no context rows", turnID)
	}
	if !kept.CheckpointID.Valid {
		return Checkpoint{}, nil
	}
	row, err := s.queries.GetHistoryCheckpoint(ctx, kept.CheckpointID)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("load history checkpoint kept before turn: %w", err)
	}
	return resolve(ctx, s.queries, row)
}

// resolve reads row as a request renders it, with the text of its summaries.
func resolve(ctx context.Context, q dbstore.Queries, row sqlc.BotHistoryCheckpoint) (Checkpoint, error) {
	checkpoint, err := fromRow(row)
	if err != nil {
		return Checkpoint{}, err
	}
	ids := checkpoint.SummaryIDs()
	pgIDs := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		pgID, err := dbpkg.ParseUUID(id)
		if err != nil {
			return Checkpoint{}, fmt.Errorf("history checkpoint %s: %w", checkpoint.ID, err)
		}
		pgIDs = append(pgIDs, pgID)
	}
	var rows []sqlc.ListHistoryCheckpointSummariesRow
	if len(pgIDs) > 0 {
		var err error
		if rows, err = q.ListHistoryCheckpointSummaries(ctx, sqlc.ListHistoryCheckpointSummariesParams{SessionID: row.SessionID, Ids: pgIDs}); err != nil {
			return Checkpoint{}, fmt.Errorf("load history checkpoint summaries: %w", err)
		}
	}
	texts := make(map[string]string, len(rows))
	for _, summary := range rows {
		texts[summary.ID.String()] = summary.Summary
	}
	for i, item := range checkpoint.Items {
		if item.SummaryID == "" {
			continue
		}
		text, ok := texts[item.SummaryID]
		if !ok {
			return Checkpoint{}, fmt.Errorf("history checkpoint %s holds summary %s, which is not a usable summary of its session", checkpoint.ID, item.SummaryID)
		}
		checkpoint.Items[i].Summary = text
	}
	return checkpoint, nil
}

func countUnseenRows(ctx context.Context, rows int) {
	counter, err := otel.Meter(meterName).Int64Counter("history.checkpoint.unseen_rows_total")
	if err != nil {
		return
	}
	counter.Add(ctx, int64(rows))
}
