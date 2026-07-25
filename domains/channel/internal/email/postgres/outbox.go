package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
	channelsqlc "github.com/memohai/memoh/domains/channel/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type outboxQueries interface {
	CreateEmailOutbox(context.Context, channelsqlc.CreateEmailOutboxParams) (channelsqlc.ChannelEmailOutbox, error)
	UpdateEmailOutboxSent(context.Context, channelsqlc.UpdateEmailOutboxSentParams) error
	UpdateEmailOutboxFailed(context.Context, channelsqlc.UpdateEmailOutboxFailedParams) error
	GetEmailOutboxByID(context.Context, pgtype.UUID) (channelsqlc.ChannelEmailOutbox, error)
	ListEmailOutboxByBot(context.Context, channelsqlc.ListEmailOutboxByBotParams) ([]channelsqlc.ChannelEmailOutbox, error)
	CountEmailOutboxByBot(context.Context, pgtype.UUID) (int64, error)
}

// OutboxStore adapts generated PostgreSQL statements to Email's outbox port.
type OutboxStore struct {
	queries outboxQueries
}

var _ emailport.OutboxStore = (*OutboxStore)(nil)

func NewOutboxStore(pool *pgxpool.Pool) *OutboxStore {
	return NewOutboxStoreWithQueries(channelsqlc.New(pool))
}

func NewOutboxStoreWithQueries(queries outboxQueries) *OutboxStore {
	return &OutboxStore{queries: queries}
}

func (s *OutboxStore) CreateOutbox(ctx context.Context, input emailport.CreateOutboxInput) (emailport.OutboxRecord, error) {
	providerID, err := db.ParseUUID(input.ProviderID)
	if err != nil {
		return emailport.OutboxRecord{}, err
	}
	botID, err := db.ParseUUID(input.BotID)
	if err != nil {
		return emailport.OutboxRecord{}, err
	}
	row, err := s.queries.CreateEmailOutbox(ctx, channelsqlc.CreateEmailOutboxParams{
		ProviderID: providerID, BotID: botID, FromAddress: input.FromAddress,
		ToAddresses: input.ToAddresses, Subject: input.Subject, BodyText: input.BodyText,
		BodyHtml: input.BodyHTML, Attachments: input.Attachments, Status: input.Status,
	})
	if err != nil {
		return emailport.OutboxRecord{}, classifyError(err)
	}
	return outboxRecord(row), nil
}

func (s *OutboxStore) MarkOutboxSent(ctx context.Context, id, messageID string) error {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return s.queries.UpdateEmailOutboxSent(ctx, channelsqlc.UpdateEmailOutboxSentParams{
		ID: parsed, MessageID: messageID,
	})
}

func (s *OutboxStore) MarkOutboxFailed(ctx context.Context, id, message string) error {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return s.queries.UpdateEmailOutboxFailed(ctx, channelsqlc.UpdateEmailOutboxFailedParams{
		ID: parsed, Error: message,
	})
}

func (s *OutboxStore) FindOutbox(ctx context.Context, id string) (emailport.OutboxRecord, error) {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return emailport.OutboxRecord{}, err
	}
	row, err := s.queries.GetEmailOutboxByID(ctx, parsed)
	if err != nil {
		return emailport.OutboxRecord{}, classifyError(err)
	}
	return outboxRecord(row), nil
}

func (s *OutboxStore) ListOutboxByBot(ctx context.Context, botID string, limit, offset int32) ([]emailport.OutboxRecord, error) {
	parsed, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListEmailOutboxByBot(ctx, channelsqlc.ListEmailOutboxByBotParams{
		BotID: parsed, Lim: limit, Off: offset,
	})
	if err != nil {
		return nil, err
	}
	records := make([]emailport.OutboxRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, outboxRecord(row))
	}
	return records, nil
}

func (s *OutboxStore) CountOutboxByBot(ctx context.Context, botID string) (int64, error) {
	parsed, err := db.ParseUUID(botID)
	if err != nil {
		return 0, err
	}
	return s.queries.CountEmailOutboxByBot(ctx, parsed)
}

func outboxRecord(row channelsqlc.ChannelEmailOutbox) emailport.OutboxRecord {
	return emailport.OutboxRecord{
		ID: row.ID.String(), ProviderID: row.ProviderID.String(), BotID: row.BotID.String(),
		MessageID: row.MessageID, FromAddress: row.FromAddress,
		ToAddresses: append([]byte(nil), row.ToAddresses...), Subject: row.Subject,
		BodyText: row.BodyText, BodyHTML: row.BodyHtml,
		Attachments: append([]byte(nil), row.Attachments...),
		Status:      row.Status, Error: row.Error, SentAt: db.TimeFromPg(row.SentAt),
		CreatedAt: db.TimeFromPg(row.CreatedAt),
	}
}
