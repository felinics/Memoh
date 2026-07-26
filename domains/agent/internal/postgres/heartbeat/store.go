package heartbeat

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/agent/automation/heartbeat/persistence"
	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type queries interface {
	CreateHeartbeatLog(context.Context, dbsqlc.CreateHeartbeatLogParams) (dbsqlc.CreateHeartbeatLogRow, error)
	CompleteHeartbeatLog(context.Context, dbsqlc.CompleteHeartbeatLogParams) (dbsqlc.AgentBotHeartbeatLog, error)
	CountHeartbeatLogsByBot(context.Context, pgtype.UUID) (int64, error)
	ListHeartbeatLogsByBot(context.Context, dbsqlc.ListHeartbeatLogsByBotParams) ([]dbsqlc.ListHeartbeatLogsByBotRow, error)
	DeleteHeartbeatLogsByBot(context.Context, pgtype.UUID) error
}

// Store adapts Agent-owner SQLC statements to heartbeat persistence.
type Store struct {
	queries queries
	bots    persistence.BotReader
}

var _ persistence.Store = (*Store)(nil)

func NewStore(queries queries, bots persistence.BotReader) *Store {
	return &Store{queries: queries, bots: bots}
}

func NewStoreFromDB(db dbsqlc.DBTX, bots persistence.BotReader) *Store {
	return NewStore(dbsqlc.New(db), bots)
}

func newStore(queries queries, bots persistence.BotReader) *Store {
	return NewStore(queries, bots)
}

func (s *Store) ListEnabledBots(ctx context.Context) ([]persistence.BotRecord, error) {
	return s.bots.ListEnabledBots(ctx)
}

func (s *Store) GetBot(ctx context.Context, id string) (persistence.BotRecord, error) {
	if _, err := db.ParseUUID(id); err != nil {
		return persistence.BotRecord{}, err
	}
	return s.bots.GetBot(ctx, id)
}

func (s *Store) CreateLog(ctx context.Context, command persistence.CreateLogCommand) (string, error) {
	botID, err := db.ParseUUID(command.BotID)
	if err != nil {
		return "", err
	}
	row, err := s.queries.CreateHeartbeatLog(ctx, dbsqlc.CreateHeartbeatLogParams{
		BotID: botID, SessionID: db.ParseUUIDOrEmpty(command.SessionID),
	})
	return row.ID.String(), err
}

func (s *Store) CompleteLog(ctx context.Context, command persistence.CompleteLogCommand) error {
	id, err := db.ParseUUID(command.ID)
	if err != nil {
		return err
	}
	_, err = s.queries.CompleteHeartbeatLog(ctx, dbsqlc.CompleteHeartbeatLogParams{
		ID: id, Status: command.Status, ResultText: command.ResultText,
		ErrorMessage: command.ErrorMessage, Usage: command.Usage,
		ModelID: db.ParseUUIDOrEmpty(command.ModelID),
	})
	return err
}

func (s *Store) CountLogsByBot(ctx context.Context, botID string) (int64, error) {
	parsed, err := db.ParseUUID(botID)
	if err != nil {
		return 0, err
	}
	return s.queries.CountHeartbeatLogsByBot(ctx, parsed)
}

func (s *Store) ListLogsByBot(ctx context.Context, page persistence.LogPage) ([]persistence.LogRecord, error) {
	parsed, err := db.ParseUUID(page.BotID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListHeartbeatLogsByBot(ctx, dbsqlc.ListHeartbeatLogsByBotParams{
		BotID: parsed, Limit: page.Limit, Offset: page.Offset,
	})
	if err != nil {
		return nil, err
	}
	items := make([]persistence.LogRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, persistence.LogRecord{
			ID: row.ID.String(), BotID: row.BotID.String(), SessionID: row.SessionID.String(),
			Status: row.Status, ResultText: row.ResultText, ErrorMessage: row.ErrorMessage,
			Usage: append([]byte(nil), row.Usage...), StartedAt: db.TimeFromPg(row.StartedAt),
			CompletedAt: db.TimeFromPg(row.CompletedAt),
		})
	}
	return items, nil
}

func (s *Store) DeleteLogsByBot(ctx context.Context, botID string) error {
	parsed, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	return s.queries.DeleteHeartbeatLogsByBot(ctx, parsed)
}
