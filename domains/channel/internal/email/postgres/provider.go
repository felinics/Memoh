package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
	channelsqlc "github.com/memohai/memoh/domains/channel/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type providerQueries interface {
	CreateEmailProvider(context.Context, channelsqlc.CreateEmailProviderParams) (channelsqlc.ChannelEmailProvider, error)
	GetEmailProviderByID(context.Context, pgtype.UUID) (channelsqlc.ChannelEmailProvider, error)
	GetEmailProviderByIDAndUser(context.Context, channelsqlc.GetEmailProviderByIDAndUserParams) (channelsqlc.ChannelEmailProvider, error)
	GetEmailProviderByNameAndUser(context.Context, channelsqlc.GetEmailProviderByNameAndUserParams) (channelsqlc.ChannelEmailProvider, error)
	ListEmailProviders(context.Context) ([]channelsqlc.ChannelEmailProvider, error)
	ListEmailProvidersByProvider(context.Context, string) ([]channelsqlc.ChannelEmailProvider, error)
	ListEmailProvidersByUser(context.Context, pgtype.UUID) ([]channelsqlc.ChannelEmailProvider, error)
	ListEmailProvidersByUserAndProvider(context.Context, channelsqlc.ListEmailProvidersByUserAndProviderParams) ([]channelsqlc.ChannelEmailProvider, error)
	UpdateEmailProviderByIDAndUser(context.Context, channelsqlc.UpdateEmailProviderByIDAndUserParams) (channelsqlc.ChannelEmailProvider, error)
	DeleteEmailProviderByIDAndUser(context.Context, channelsqlc.DeleteEmailProviderByIDAndUserParams) error
}

// ProviderStore adapts generated PostgreSQL statements to Email's provider port.
type ProviderStore struct {
	queries providerQueries
}

var _ emailport.ProviderStore = (*ProviderStore)(nil)

func NewProviderStore(pool *pgxpool.Pool) *ProviderStore {
	return NewProviderStoreWithQueries(channelsqlc.New(pool))
}

func NewProviderStoreWithQueries(queries providerQueries) *ProviderStore {
	return &ProviderStore{queries: queries}
}

func (s *ProviderStore) CreateProvider(ctx context.Context, input emailport.CreateProviderInput) (emailport.ProviderRecord, error) {
	userID, err := db.ParseUUID(input.UserID)
	if err != nil {
		return emailport.ProviderRecord{}, err
	}
	row, err := s.queries.CreateEmailProvider(ctx, channelsqlc.CreateEmailProviderParams{
		UserID: userID, Name: input.Name, Provider: input.Provider, Config: input.Config,
	})
	if err != nil {
		return emailport.ProviderRecord{}, classifyError(err)
	}
	return providerRecord(row), nil
}

func (s *ProviderStore) FindProvider(ctx context.Context, id string) (emailport.ProviderRecord, error) {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return emailport.ProviderRecord{}, err
	}
	row, err := s.queries.GetEmailProviderByID(ctx, parsed)
	if err != nil {
		return emailport.ProviderRecord{}, classifyError(err)
	}
	return providerRecord(row), nil
}

func (s *ProviderStore) FindProviderForUser(ctx context.Context, userID, id string) (emailport.ProviderRecord, error) {
	parsedUserID, err := db.ParseUUID(userID)
	if err != nil {
		return emailport.ProviderRecord{}, err
	}
	parsedID, err := db.ParseUUID(id)
	if err != nil {
		return emailport.ProviderRecord{}, err
	}
	row, err := s.queries.GetEmailProviderByIDAndUser(ctx, channelsqlc.GetEmailProviderByIDAndUserParams{
		ID: parsedID, UserID: parsedUserID,
	})
	if err != nil {
		return emailport.ProviderRecord{}, classifyError(err)
	}
	return providerRecord(row), nil
}

func (s *ProviderStore) FindProviderByName(ctx context.Context, userID, name string) (emailport.ProviderRecord, error) {
	parsedUserID, err := db.ParseUUID(userID)
	if err != nil {
		return emailport.ProviderRecord{}, err
	}
	row, err := s.queries.GetEmailProviderByNameAndUser(ctx, channelsqlc.GetEmailProviderByNameAndUserParams{
		UserID: parsedUserID, Name: name,
	})
	if err != nil {
		return emailport.ProviderRecord{}, classifyError(err)
	}
	return providerRecord(row), nil
}

func (s *ProviderStore) ListProviders(ctx context.Context, provider string) ([]emailport.ProviderRecord, error) {
	var (
		rows []channelsqlc.ChannelEmailProvider
		err  error
	)
	if provider == "" {
		rows, err = s.queries.ListEmailProviders(ctx)
	} else {
		rows, err = s.queries.ListEmailProvidersByProvider(ctx, provider)
	}
	if err != nil {
		return nil, err
	}
	return providerRecords(rows), nil
}

func (s *ProviderStore) ListProvidersForUser(ctx context.Context, userID, provider string) ([]emailport.ProviderRecord, error) {
	parsedUserID, err := db.ParseUUID(userID)
	if err != nil {
		return nil, err
	}
	var rows []channelsqlc.ChannelEmailProvider
	if provider == "" {
		rows, err = s.queries.ListEmailProvidersByUser(ctx, parsedUserID)
	} else {
		rows, err = s.queries.ListEmailProvidersByUserAndProvider(ctx, channelsqlc.ListEmailProvidersByUserAndProviderParams{
			UserID: parsedUserID, Provider: provider,
		})
	}
	if err != nil {
		return nil, err
	}
	return providerRecords(rows), nil
}

func (s *ProviderStore) UpdateProvider(ctx context.Context, input emailport.UpdateProviderInput) (emailport.ProviderRecord, error) {
	id, err := db.ParseUUID(input.ID)
	if err != nil {
		return emailport.ProviderRecord{}, err
	}
	userID, err := db.ParseUUID(input.UserID)
	if err != nil {
		return emailport.ProviderRecord{}, err
	}
	row, err := s.queries.UpdateEmailProviderByIDAndUser(ctx, channelsqlc.UpdateEmailProviderByIDAndUserParams{
		ID: id, UserID: userID, Name: input.Name, Provider: input.Provider, Config: input.Config,
	})
	if err != nil {
		return emailport.ProviderRecord{}, classifyError(err)
	}
	return providerRecord(row), nil
}

func (s *ProviderStore) DeleteProvider(ctx context.Context, userID, id string) error {
	parsedUserID, err := db.ParseUUID(userID)
	if err != nil {
		return err
	}
	parsedID, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return s.queries.DeleteEmailProviderByIDAndUser(ctx, channelsqlc.DeleteEmailProviderByIDAndUserParams{
		ID: parsedID, UserID: parsedUserID,
	})
}

func providerRecords(rows []channelsqlc.ChannelEmailProvider) []emailport.ProviderRecord {
	records := make([]emailport.ProviderRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, providerRecord(row))
	}
	return records
}

func providerRecord(row channelsqlc.ChannelEmailProvider) emailport.ProviderRecord {
	return emailport.ProviderRecord{
		ID: row.ID.String(), UserID: row.UserID.String(), Name: row.Name, Provider: row.Provider,
		Config:    append([]byte(nil), row.Config...),
		CreatedAt: db.TimeFromPg(row.CreatedAt), UpdatedAt: db.TimeFromPg(row.UpdatedAt),
	}
}
