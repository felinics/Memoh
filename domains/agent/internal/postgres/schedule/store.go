package schedule

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	schedulepersistence "github.com/memohai/memoh/domains/agent/automation/schedule/persistence"
	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type queries interface {
	ListEnabledSchedules(context.Context) ([]dbsqlc.AgentSchedule, error)
	CreateSchedule(context.Context, dbsqlc.CreateScheduleParams) (dbsqlc.AgentSchedule, error)
	GetScheduleByID(context.Context, pgtype.UUID) (dbsqlc.AgentSchedule, error)
	ListSchedulesByBot(context.Context, pgtype.UUID) ([]dbsqlc.AgentSchedule, error)
	UpdateSchedule(context.Context, dbsqlc.UpdateScheduleParams) (dbsqlc.AgentSchedule, error)
	DeleteSchedule(context.Context, pgtype.UUID) error
	IncrementScheduleCalls(context.Context, pgtype.UUID) (dbsqlc.AgentSchedule, error)
	CreateScheduleLog(context.Context, dbsqlc.CreateScheduleLogParams) (dbsqlc.CreateScheduleLogRow, error)
	CompleteScheduleLog(context.Context, dbsqlc.CompleteScheduleLogParams) (dbsqlc.AgentScheduleLog, error)
	CountScheduleLogsByBot(context.Context, pgtype.UUID) (int64, error)
	ListScheduleLogsByBot(context.Context, dbsqlc.ListScheduleLogsByBotParams) ([]dbsqlc.ListScheduleLogsByBotRow, error)
	CountScheduleLogsBySchedule(context.Context, pgtype.UUID) (int64, error)
	ListScheduleLogsBySchedule(context.Context, dbsqlc.ListScheduleLogsByScheduleParams) ([]dbsqlc.ListScheduleLogsByScheduleRow, error)
	DeleteScheduleLogsByBot(context.Context, pgtype.UUID) error
}

// Store adapts Agent-owner SQLC statements to schedule persistence.
type Store struct {
	queries queries
	bots    schedulepersistence.BotReader
}

var _ schedulepersistence.Store = (*Store)(nil)

func NewStore(queries queries, bots schedulepersistence.BotReader) *Store {
	return &Store{queries: queries, bots: bots}
}

func NewStoreFromDB(db dbsqlc.DBTX, bots schedulepersistence.BotReader) *Store {
	return NewStore(dbsqlc.New(db), bots)
}

func newStore(queries queries, bots schedulepersistence.BotReader) *Store {
	return NewStore(queries, bots)
}

func (s *Store) ListEnabled(ctx context.Context) ([]schedulepersistence.Record, error) {
	rows, err := s.queries.ListEnabledSchedules(ctx)
	if err != nil {
		return nil, err
	}
	return scheduleRecords(rows), nil
}

func (s *Store) Create(ctx context.Context, command schedulepersistence.CreateCommand) (schedulepersistence.Record, error) {
	botID, err := db.ParseUUID(command.BotID)
	if err != nil {
		return schedulepersistence.Record{}, err
	}
	row, err := s.queries.CreateSchedule(ctx, dbsqlc.CreateScheduleParams{
		Name: command.Name, Description: command.Description, Pattern: command.Pattern,
		MaxCalls: optionalInt4(command.MaxCalls), Enabled: command.Enabled,
		Command: command.Command, BotID: botID,
	})
	if err != nil {
		return schedulepersistence.Record{}, mapNotFound(err)
	}
	return scheduleRecord(row), nil
}

func (s *Store) Get(ctx context.Context, id string) (schedulepersistence.Record, error) {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return schedulepersistence.Record{}, err
	}
	row, err := s.queries.GetScheduleByID(ctx, parsed)
	if err != nil {
		return schedulepersistence.Record{}, mapNotFound(err)
	}
	return scheduleRecord(row), nil
}

func (s *Store) ListByBot(ctx context.Context, botID string) ([]schedulepersistence.Record, error) {
	parsed, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListSchedulesByBot(ctx, parsed)
	if err != nil {
		return nil, err
	}
	return scheduleRecords(rows), nil
}

func (s *Store) Update(ctx context.Context, command schedulepersistence.UpdateCommand) (schedulepersistence.Record, error) {
	parsed, err := db.ParseUUID(command.ID)
	if err != nil {
		return schedulepersistence.Record{}, err
	}
	row, err := s.queries.UpdateSchedule(ctx, dbsqlc.UpdateScheduleParams{
		ID: parsed, Name: command.Name, Description: command.Description,
		Pattern: command.Pattern, MaxCalls: optionalInt4(command.MaxCalls),
		Enabled: command.Enabled, Command: command.Command,
	})
	if err != nil {
		return schedulepersistence.Record{}, mapNotFound(err)
	}
	return scheduleRecord(row), nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return s.queries.DeleteSchedule(ctx, parsed)
}

func (s *Store) IncrementCalls(ctx context.Context, id string) (schedulepersistence.Record, error) {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return schedulepersistence.Record{}, err
	}
	row, err := s.queries.IncrementScheduleCalls(ctx, parsed)
	if err != nil {
		return schedulepersistence.Record{}, mapNotFound(err)
	}
	return scheduleRecord(row), nil
}

func (s *Store) GetBot(ctx context.Context, id string) (schedulepersistence.BotRecord, error) {
	if _, err := db.ParseUUID(id); err != nil {
		return schedulepersistence.BotRecord{}, err
	}
	return s.bots.GetBot(ctx, id)
}

func (s *Store) CreateLog(ctx context.Context, command schedulepersistence.CreateLogCommand) (string, error) {
	scheduleID, err := db.ParseUUID(command.ScheduleID)
	if err != nil {
		return "", err
	}
	botID, err := db.ParseUUID(command.BotID)
	if err != nil {
		return "", err
	}
	row, err := s.queries.CreateScheduleLog(ctx, dbsqlc.CreateScheduleLogParams{
		ScheduleID: scheduleID,
		BotID:      botID,
		SessionID:  db.ParseUUIDOrEmpty(command.SessionID),
	})
	return row.ID.String(), err
}

func (s *Store) CompleteLog(ctx context.Context, command schedulepersistence.CompleteLogCommand) error {
	id, err := db.ParseUUID(command.ID)
	if err != nil {
		return err
	}
	_, err = s.queries.CompleteScheduleLog(ctx, dbsqlc.CompleteScheduleLogParams{
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
	return s.queries.CountScheduleLogsByBot(ctx, parsed)
}

func (s *Store) ListLogsByBot(ctx context.Context, page schedulepersistence.LogPage) ([]schedulepersistence.LogRecord, error) {
	parsed, err := db.ParseUUID(page.ID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListScheduleLogsByBot(ctx, dbsqlc.ListScheduleLogsByBotParams{
		BotID: parsed, Limit: page.Limit, Offset: page.Offset,
	})
	if err != nil {
		return nil, err
	}
	items := make([]schedulepersistence.LogRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, logRecord(
			row.ID, row.ScheduleID, row.BotID, row.SessionID, row.Status,
			row.ResultText, row.ErrorMessage, row.Usage, row.StartedAt, row.CompletedAt,
		))
	}
	return items, nil
}

func (s *Store) CountLogsBySchedule(ctx context.Context, scheduleID string) (int64, error) {
	parsed, err := db.ParseUUID(scheduleID)
	if err != nil {
		return 0, err
	}
	return s.queries.CountScheduleLogsBySchedule(ctx, parsed)
}

func (s *Store) ListLogsBySchedule(ctx context.Context, page schedulepersistence.LogPage) ([]schedulepersistence.LogRecord, error) {
	parsed, err := db.ParseUUID(page.ID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListScheduleLogsBySchedule(ctx, dbsqlc.ListScheduleLogsByScheduleParams{
		ScheduleID: parsed, Limit: page.Limit, Offset: page.Offset,
	})
	if err != nil {
		return nil, err
	}
	items := make([]schedulepersistence.LogRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, logRecord(
			row.ID, row.ScheduleID, row.BotID, row.SessionID, row.Status,
			row.ResultText, row.ErrorMessage, row.Usage, row.StartedAt, row.CompletedAt,
		))
	}
	return items, nil
}

func (s *Store) DeleteLogsByBot(ctx context.Context, botID string) error {
	parsed, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	return s.queries.DeleteScheduleLogsByBot(ctx, parsed)
}

func scheduleRecords(rows []dbsqlc.AgentSchedule) []schedulepersistence.Record {
	items := make([]schedulepersistence.Record, 0, len(rows))
	for _, row := range rows {
		items = append(items, scheduleRecord(row))
	}
	return items
}

func scheduleRecord(row dbsqlc.AgentSchedule) schedulepersistence.Record {
	var maxCalls *int
	if row.MaxCalls.Valid {
		value := int(row.MaxCalls.Int32)
		maxCalls = &value
	}
	return schedulepersistence.Record{
		ID: row.ID.String(), Name: row.Name, Description: row.Description,
		Pattern: row.Pattern, MaxCalls: maxCalls, CurrentCalls: int(row.CurrentCalls),
		CreatedAt: db.TimeFromPg(row.CreatedAt), UpdatedAt: db.TimeFromPg(row.UpdatedAt),
		Enabled: row.Enabled, Command: row.Command, BotID: row.BotID.String(),
	}
}

func logRecord(
	id, scheduleID, botID, sessionID pgtype.UUID,
	status, resultText, errorMessage string,
	usage []byte,
	startedAt, completedAt pgtype.Timestamptz,
) schedulepersistence.LogRecord {
	return schedulepersistence.LogRecord{
		ID: id.String(), ScheduleID: scheduleID.String(), BotID: botID.String(),
		SessionID: sessionID.String(), Status: status, ResultText: resultText,
		ErrorMessage: errorMessage, Usage: append([]byte(nil), usage...),
		StartedAt: db.TimeFromPg(startedAt), CompletedAt: db.TimeFromPg(completedAt),
	}
}

func optionalInt4(value *int) pgtype.Int4 {
	if value == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*value), Valid: true} //nolint:gosec // Consumer validates the int32 range.
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return schedulepersistence.ErrNotFound
	}
	return err
}
