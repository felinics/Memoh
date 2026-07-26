package usage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	usagepersistence "github.com/memohai/memoh/domains/agent/chat/usage/persistence"
	agentsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"

	"github.com/memohai/memoh/internal/db"
)

// SQLCQueries is the generated-query surface required by the usage read model.
type SQLCQueries interface {
	GetSessionByID(context.Context, pgtype.UUID) (agentsqlc.AgentBotSession, error)
	GetLatestSessionIDByBot(context.Context, pgtype.UUID) (pgtype.UUID, error)
	CountMessagesBySession(context.Context, pgtype.UUID) (int64, error)
	GetLatestAssistantUsage(context.Context, pgtype.UUID) (int64, error)
	GetSessionCacheStats(context.Context, pgtype.UUID) (agentsqlc.GetSessionCacheStatsRow, error)
	GetSessionUsedSkills(context.Context, pgtype.UUID) ([]string, error)
	GetTokenUsageByDayAndType(context.Context, agentsqlc.GetTokenUsageByDayAndTypeParams) ([]agentsqlc.GetTokenUsageByDayAndTypeRow, error)
	GetTokenUsageByModel(context.Context, agentsqlc.GetTokenUsageByModelParams) ([]agentsqlc.GetTokenUsageByModelRow, error)
	ListTokenUsageRecords(context.Context, agentsqlc.ListTokenUsageRecordsParams) ([]agentsqlc.ListTokenUsageRecordsRow, error)
	CountTokenUsageRecords(context.Context, agentsqlc.CountTokenUsageRecordsParams) (int64, error)
}

// Reader adapts generated PostgreSQL queries to the persistence-neutral usage model.
type Reader struct {
	queries SQLCQueries
}

func New(pool *pgxpool.Pool) *Reader {
	return newReader(agentsqlc.New(pool))
}

func newReader(queries SQLCQueries) *Reader {
	return &Reader{queries: queries}
}

func (r *Reader) GetSession(ctx context.Context, sessionID string) (usagepersistence.Session, error) {
	id, err := parseUUID(sessionID, "session ID")
	if err != nil {
		return usagepersistence.Session{}, err
	}
	row, err := r.queries.GetSessionByID(ctx, id)
	if err != nil {
		return usagepersistence.Session{}, mapError(err)
	}
	return usagepersistence.Session{
		ID:              db.UUIDString(row.ID),
		BotID:           db.UUIDString(row.BotID),
		Type:            row.Type,
		SessionMode:     row.SessionMode,
		RuntimeType:     row.RuntimeType,
		CreatedByUserID: db.UUIDString(row.CreatedByUserID),
	}, nil
}

func (r *Reader) GetLatestSessionIDByBot(ctx context.Context, botID string) (string, error) {
	id, err := parseUUID(botID, "bot ID")
	if err != nil {
		return "", err
	}
	sessionID, err := r.queries.GetLatestSessionIDByBot(ctx, id)
	if err != nil {
		return "", mapError(err)
	}
	if !sessionID.Valid {
		return "", usagepersistence.ErrNotFound
	}
	return db.UUIDString(sessionID), nil
}

func (r *Reader) CountMessagesBySession(ctx context.Context, sessionID string) (int64, error) {
	id, err := parseUUID(sessionID, "session ID")
	if err != nil {
		return 0, err
	}
	count, err := r.queries.CountMessagesBySession(ctx, id)
	return count, mapError(err)
}

func (r *Reader) GetLatestAssistantUsage(ctx context.Context, sessionID string) (int64, error) {
	id, err := parseUUID(sessionID, "session ID")
	if err != nil {
		return 0, err
	}
	value, err := r.queries.GetLatestAssistantUsage(ctx, id)
	return value, mapError(err)
}

func (r *Reader) GetSessionCacheStats(ctx context.Context, sessionID string) (usagepersistence.CacheStats, error) {
	id, err := parseUUID(sessionID, "session ID")
	if err != nil {
		return usagepersistence.CacheStats{}, err
	}
	row, err := r.queries.GetSessionCacheStats(ctx, id)
	if err != nil {
		return usagepersistence.CacheStats{}, mapError(err)
	}
	return usagepersistence.CacheStats{
		TotalInputTokens: row.TotalInputTokens,
		CacheReadTokens:  row.CacheReadTokens,
	}, nil
}

func (r *Reader) GetSessionUsedSkills(ctx context.Context, sessionID string) ([]string, error) {
	id, err := parseUUID(sessionID, "session ID")
	if err != nil {
		return nil, err
	}
	skills, err := r.queries.GetSessionUsedSkills(ctx, id)
	return skills, mapError(err)
}

func (r *Reader) GetTokenUsageByDayAndType(ctx context.Context, botID string, from, to time.Time) ([]usagepersistence.Daily, error) {
	return r.GetDaily(ctx, usagepersistence.Filter{BotID: botID, From: from, To: to})
}

func (r *Reader) GetTokenUsageByModel(ctx context.Context, botID string, from, to time.Time) ([]usagepersistence.Model, error) {
	return r.GetByModel(ctx, usagepersistence.Filter{BotID: botID, From: from, To: to})
}

func (r *Reader) GetDaily(ctx context.Context, filter usagepersistence.Filter) ([]usagepersistence.Daily, error) {
	params, err := dailyParams(filter)
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.GetTokenUsageByDayAndType(ctx, params)
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]usagepersistence.Daily, len(rows))
	for i, row := range rows {
		items[i] = usagepersistence.Daily{
			SessionType:     row.SessionType,
			Day:             optionalTime(row.Day.Time, row.Day.Valid),
			InputTokens:     row.InputTokens,
			OutputTokens:    row.OutputTokens,
			CacheReadTokens: row.CacheReadTokens,
			ReasoningTokens: row.ReasoningTokens,
		}
	}
	return items, nil
}

func (r *Reader) GetByModel(ctx context.Context, filter usagepersistence.Filter) ([]usagepersistence.Model, error) {
	params, err := modelParams(filter)
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.GetTokenUsageByModel(ctx, params)
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]usagepersistence.Model, len(rows))
	for i, row := range rows {
		items[i] = usagepersistence.Model{
			ModelID:      db.UUIDString(row.ModelID),
			InputTokens:  row.InputTokens,
			OutputTokens: row.OutputTokens,
		}
	}
	return items, nil
}

func (r *Reader) ListRecords(ctx context.Context, filter usagepersistence.Filter, pagination usagepersistence.Pagination) (usagepersistence.Page, error) {
	params, err := recordParams(filter, pagination)
	if err != nil {
		return usagepersistence.Page{}, err
	}
	rows, err := r.queries.ListTokenUsageRecords(ctx, params)
	if err != nil {
		return usagepersistence.Page{}, mapError(err)
	}
	countParams := agentsqlc.CountTokenUsageRecordsParams{
		BotID:       params.BotID,
		FromTime:    params.FromTime,
		ToTime:      params.ToTime,
		ModelID:     params.ModelID,
		SessionType: params.SessionType,
	}
	total, err := r.queries.CountTokenUsageRecords(ctx, countParams)
	if err != nil {
		return usagepersistence.Page{}, fmt.Errorf("%w: %w", usagepersistence.ErrCountRecords, mapError(err))
	}
	items := make([]usagepersistence.Record, len(rows))
	for i, row := range rows {
		items[i] = usagepersistence.Record{
			ID:              db.UUIDString(row.ID),
			CreatedAt:       optionalTime(row.CreatedAt.Time, row.CreatedAt.Valid),
			SessionID:       db.UUIDString(row.SessionID),
			SessionType:     row.SessionType,
			ModelID:         db.UUIDString(row.ModelID),
			InputTokens:     row.InputTokens,
			OutputTokens:    row.OutputTokens,
			CacheReadTokens: row.CacheReadTokens,
			ReasoningTokens: row.ReasoningTokens,
		}
	}
	return usagepersistence.Page{Items: items, Total: total}, nil
}

func dailyParams(filter usagepersistence.Filter) (agentsqlc.GetTokenUsageByDayAndTypeParams, error) {
	botID, modelID, sessionType, err := filterParams(filter)
	if err != nil {
		return agentsqlc.GetTokenUsageByDayAndTypeParams{}, err
	}
	return agentsqlc.GetTokenUsageByDayAndTypeParams{
		BotID:       botID,
		FromTime:    pgtype.Timestamptz{Time: filter.From, Valid: true},
		ToTime:      pgtype.Timestamptz{Time: filter.To, Valid: true},
		ModelID:     modelID,
		SessionType: sessionType,
	}, nil
}

func modelParams(filter usagepersistence.Filter) (agentsqlc.GetTokenUsageByModelParams, error) {
	botID, _, sessionType, err := filterParams(filter)
	if err != nil {
		return agentsqlc.GetTokenUsageByModelParams{}, err
	}
	return agentsqlc.GetTokenUsageByModelParams{
		BotID:       botID,
		FromTime:    pgtype.Timestamptz{Time: filter.From, Valid: true},
		ToTime:      pgtype.Timestamptz{Time: filter.To, Valid: true},
		SessionType: sessionType,
	}, nil
}

func recordParams(filter usagepersistence.Filter, pagination usagepersistence.Pagination) (agentsqlc.ListTokenUsageRecordsParams, error) {
	if pagination.Limit < 0 || pagination.Limit > math.MaxInt32 || pagination.Offset < 0 || pagination.Offset > math.MaxInt32 {
		return agentsqlc.ListTokenUsageRecordsParams{}, errors.New("usage pagination is out of range")
	}
	botID, modelID, sessionType, err := filterParams(filter)
	if err != nil {
		return agentsqlc.ListTokenUsageRecordsParams{}, err
	}
	return agentsqlc.ListTokenUsageRecordsParams{
		BotID:       botID,
		FromTime:    pgtype.Timestamptz{Time: filter.From, Valid: true},
		ToTime:      pgtype.Timestamptz{Time: filter.To, Valid: true},
		ModelID:     modelID,
		SessionType: sessionType,
		PageOffset:  int32(pagination.Offset),
		PageLimit:   int32(pagination.Limit),
	}, nil
}

func filterParams(filter usagepersistence.Filter) (pgtype.UUID, pgtype.UUID, pgtype.Text, error) {
	botID, err := parseUUID(filter.BotID, "bot ID")
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.Text{}, err
	}
	var modelID pgtype.UUID
	if value := strings.TrimSpace(filter.ModelID); value != "" {
		modelID, err = parseUUID(value, "model ID")
		if err != nil {
			return pgtype.UUID{}, pgtype.UUID{}, pgtype.Text{}, err
		}
	}
	var sessionType pgtype.Text
	if value := strings.TrimSpace(filter.SessionType); value != "" {
		sessionType = pgtype.Text{String: value, Valid: true}
	}
	return botID, modelID, sessionType, nil
}

func parseUUID(value, field string) (pgtype.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid %s: %w", field, err)
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, nil
}

func optionalTime(value time.Time, valid bool) time.Time {
	if !valid {
		return time.Time{}
	}
	return value
}

func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return usagepersistence.ErrNotFound
	}
	return err
}

var _ usagepersistence.Reader = (*Reader)(nil)
