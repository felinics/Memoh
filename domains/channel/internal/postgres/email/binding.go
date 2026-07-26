package email

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
	channelsqlc "github.com/memohai/memoh/domains/channel/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type bindingQueries interface {
	CreateBotEmailBinding(context.Context, channelsqlc.CreateBotEmailBindingParams) (channelsqlc.ChannelBotEmailBinding, error)
	GetBotEmailBindingByID(context.Context, pgtype.UUID) (channelsqlc.ChannelBotEmailBinding, error)
	ListBotEmailBindings(context.Context, pgtype.UUID) ([]channelsqlc.ChannelBotEmailBinding, error)
	ListReadableBindingsByProvider(context.Context, pgtype.UUID) ([]channelsqlc.ChannelBotEmailBinding, error)
	UpdateBotEmailBinding(context.Context, channelsqlc.UpdateBotEmailBindingParams) (channelsqlc.ChannelBotEmailBinding, error)
	DeleteBotEmailBinding(context.Context, pgtype.UUID) error
}

// BindingStore adapts generated PostgreSQL statements to Email's binding port.
type BindingStore struct {
	queries bindingQueries
}

var _ emailport.BindingStore = (*BindingStore)(nil)

func NewBindingStore(pool *pgxpool.Pool) *BindingStore {
	return NewBindingStoreWithQueries(channelsqlc.New(pool))
}

func NewBindingStoreWithQueries(queries bindingQueries) *BindingStore {
	return &BindingStore{queries: queries}
}

func (s *BindingStore) CreateBinding(ctx context.Context, input emailport.CreateBindingInput) (emailport.BindingRecord, error) {
	botID, err := db.ParseUUID(input.BotID)
	if err != nil {
		return emailport.BindingRecord{}, err
	}
	providerID, err := db.ParseUUID(input.EmailProviderID)
	if err != nil {
		return emailport.BindingRecord{}, err
	}
	row, err := s.queries.CreateBotEmailBinding(ctx, channelsqlc.CreateBotEmailBindingParams{
		BotID: botID, EmailProviderID: providerID, EmailAddress: input.EmailAddress,
		CanRead: input.CanRead, CanWrite: input.CanWrite, CanDelete: input.CanDelete, Config: input.Config,
	})
	if err != nil {
		return emailport.BindingRecord{}, classifyError(err)
	}
	return bindingRecord(row), nil
}

func (s *BindingStore) FindBinding(ctx context.Context, id string) (emailport.BindingRecord, error) {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return emailport.BindingRecord{}, err
	}
	row, err := s.queries.GetBotEmailBindingByID(ctx, parsed)
	if err != nil {
		return emailport.BindingRecord{}, classifyError(err)
	}
	return bindingRecord(row), nil
}

func (s *BindingStore) ListBindings(ctx context.Context, botID string) ([]emailport.BindingRecord, error) {
	parsed, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotEmailBindings(ctx, parsed)
	if err != nil {
		return nil, err
	}
	return bindingRecords(rows), nil
}

func (s *BindingStore) ListReadableBindings(ctx context.Context, providerID string) ([]emailport.BindingRecord, error) {
	parsed, err := db.ParseUUID(providerID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListReadableBindingsByProvider(ctx, parsed)
	if err != nil {
		return nil, err
	}
	return bindingRecords(rows), nil
}

func (s *BindingStore) UpdateBinding(ctx context.Context, input emailport.UpdateBindingInput) (emailport.BindingRecord, error) {
	id, err := db.ParseUUID(input.ID)
	if err != nil {
		return emailport.BindingRecord{}, err
	}
	row, err := s.queries.UpdateBotEmailBinding(ctx, channelsqlc.UpdateBotEmailBindingParams{
		ID: id, EmailAddress: input.EmailAddress, CanRead: input.CanRead,
		CanWrite: input.CanWrite, CanDelete: input.CanDelete, Config: input.Config,
	})
	if err != nil {
		return emailport.BindingRecord{}, classifyError(err)
	}
	return bindingRecord(row), nil
}

func (s *BindingStore) DeleteBinding(ctx context.Context, id string) error {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return s.queries.DeleteBotEmailBinding(ctx, parsed)
}

func bindingRecords(rows []channelsqlc.ChannelBotEmailBinding) []emailport.BindingRecord {
	records := make([]emailport.BindingRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, bindingRecord(row))
	}
	return records
}

func bindingRecord(row channelsqlc.ChannelBotEmailBinding) emailport.BindingRecord {
	return emailport.BindingRecord{
		ID: row.ID.String(), BotID: row.BotID.String(), EmailProviderID: row.EmailProviderID.String(),
		EmailAddress: row.EmailAddress, CanRead: row.CanRead, CanWrite: row.CanWrite, CanDelete: row.CanDelete,
		Config:    append([]byte(nil), row.Config...),
		CreatedAt: db.TimeFromPg(row.CreatedAt), UpdatedAt: db.TimeFromPg(row.UpdatedAt),
	}
}
