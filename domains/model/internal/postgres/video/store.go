package video

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	modeldomain "github.com/memohai/memoh/domains/model"
	videoport "github.com/memohai/memoh/domains/model/internal/port/video"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type legacyQueries interface {
	GetProviderByClientType(context.Context, string) (dbsqlc.ModelProvider, error)
	GetProviderByID(context.Context, pgtype.UUID) (dbsqlc.ModelProvider, error)
	GetVideoModelWithProvider(context.Context, pgtype.UUID) (dbsqlc.GetVideoModelWithProviderRow, error)
	ListVideoModels(context.Context) ([]dbsqlc.ListVideoModelsRow, error)
	ListVideoModelsByProviderID(context.Context, pgtype.UUID) ([]dbsqlc.ModelModel, error)
	ListVideoProviders(context.Context) ([]dbsqlc.ModelProvider, error)
	UpdateModel(context.Context, dbsqlc.UpdateModelParams) (dbsqlc.ModelModel, error)
	UpsertRegistryModel(context.Context, dbsqlc.UpsertRegistryModelParams) (dbsqlc.ModelModel, error)
}

// Store adapts the current generated statements to the video persistence port.
type Store struct {
	queries legacyQueries
}

var (
	_ videoport.Store        = (*Store)(nil)
	_ videoport.CatalogStore = (*Store)(nil)
)

// NewStore creates a postgres-backed video store from a connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{queries: dbsqlc.New(pool)}
}

// NewStoreWithQueries creates a store with an injected query surface (tests).
func NewStoreWithQueries(queries legacyQueries) *Store {
	return &Store{queries: queries}
}

func (s *Store) ListProviders(ctx context.Context) ([]videoport.ProviderRecord, error) {
	rows, err := s.queries.ListVideoProviders(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]videoport.ProviderRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, providerRecord(row))
	}
	return items, nil
}

func (s *Store) GetProvider(ctx context.Context, id string) (videoport.ProviderRecord, error) {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return videoport.ProviderRecord{}, err
	}
	row, err := s.queries.GetProviderByID(ctx, parsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return videoport.ProviderRecord{}, videoport.ErrProviderNotFound
	}
	if err != nil {
		return videoport.ProviderRecord{}, err
	}
	return providerRecord(row), nil
}

func (s *Store) GetProviderByClientType(ctx context.Context, clientType modeldomain.ClientType) (videoport.ProviderRecord, error) {
	row, err := s.queries.GetProviderByClientType(ctx, string(clientType))
	if errors.Is(err, pgx.ErrNoRows) {
		return videoport.ProviderRecord{}, videoport.ErrProviderNotFound
	}
	if err != nil {
		return videoport.ProviderRecord{}, err
	}
	return providerRecord(row), nil
}

func (s *Store) ListModels(ctx context.Context) ([]videoport.ModelRecord, error) {
	rows, err := s.queries.ListVideoModels(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]videoport.ModelRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, listedModelRecord(row))
	}
	return items, nil
}

func (s *Store) ListModelsByProvider(ctx context.Context, providerID string) ([]videoport.ModelRecord, error) {
	parsed, err := db.ParseUUID(providerID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListVideoModelsByProviderID(ctx, parsed)
	if err != nil {
		return nil, err
	}
	items := make([]videoport.ModelRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, modelRecord(row))
	}
	return items, nil
}

func (s *Store) GetModel(ctx context.Context, id string) (videoport.ModelRecord, error) {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return videoport.ModelRecord{}, err
	}
	row, err := s.queries.GetVideoModelWithProvider(ctx, parsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return videoport.ModelRecord{}, videoport.ErrModelNotFound
	}
	if err != nil {
		return videoport.ModelRecord{}, err
	}
	return modelWithProviderRecord(row), nil
}

func (s *Store) UpdateVideoModel(ctx context.Context, input videoport.UpdateModelInput) (videoport.ModelRecord, error) {
	id, err := db.ParseUUID(input.ID)
	if err != nil {
		return videoport.ModelRecord{}, err
	}
	providerID, err := db.ParseUUID(input.ProviderID)
	if err != nil {
		return videoport.ModelRecord{}, err
	}
	row, err := s.queries.UpdateModel(ctx, dbsqlc.UpdateModelParams{
		ID: id, ModelID: input.ModelID, Name: optionalText(input.Name), ProviderID: providerID,
		Type: string(input.Type), Enable: input.Enable, Config: cloneBytes(input.Config),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return videoport.ModelRecord{}, videoport.ErrModelNotFound
	}
	if err != nil {
		return videoport.ModelRecord{}, err
	}
	return modelRecord(row), nil
}

func (s *Store) UpsertVideoCatalogModel(ctx context.Context, input videoport.CatalogModelInput) error {
	providerID, err := db.ParseUUID(input.ProviderID)
	if err != nil {
		return err
	}
	_, err = s.queries.UpsertRegistryModel(ctx, dbsqlc.UpsertRegistryModelParams{
		ModelID: input.ModelID, Name: optionalText(input.Name), ProviderID: providerID,
		Type: string(input.Type), Config: cloneBytes(input.Config),
	})
	return err
}

func providerRecord(row dbsqlc.ModelProvider) videoport.ProviderRecord {
	return videoport.ProviderRecord{
		ID: uuidString(row.ID), Name: row.Name, ClientType: modeldomain.ClientType(row.ClientType), Icon: textString(row.Icon),
		Enable: row.Enable, Config: cloneBytes(row.Config), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func listedModelRecord(row dbsqlc.ListVideoModelsRow) videoport.ModelRecord {
	return videoport.ModelRecord{
		ID: uuidString(row.ID), ModelID: row.ModelID, Name: textString(row.Name), ProviderID: uuidString(row.ProviderID),
		ProviderType: modeldomain.ClientType(row.ProviderType), Config: cloneBytes(row.Config),
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func modelWithProviderRecord(row dbsqlc.GetVideoModelWithProviderRow) videoport.ModelRecord {
	return videoport.ModelRecord{
		ID: uuidString(row.ID), ModelID: row.ModelID, Name: textString(row.Name), ProviderID: uuidString(row.ProviderID),
		Type: modeldomain.ModelType(row.Type), Enable: row.Enable, Config: cloneBytes(row.Config),
		ProviderType: modeldomain.ClientType(row.ProviderType), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func modelRecord(row dbsqlc.ModelModel) videoport.ModelRecord {
	return videoport.ModelRecord{
		ID: uuidString(row.ID), ModelID: row.ModelID, Name: textString(row.Name), ProviderID: uuidString(row.ProviderID),
		Type: modeldomain.ModelType(row.Type), Enable: row.Enable, Config: cloneBytes(row.Config),
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func textString(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func optionalText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	out := make([]byte, len(value))
	copy(out, value)
	return out
}
