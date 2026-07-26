package catalog

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	modeldomain "github.com/memohai/memoh/domains/model"
	catalogport "github.com/memohai/memoh/domains/model/internal/port/catalog"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type modelQueries interface {
	CreateModel(context.Context, dbsqlc.CreateModelParams) (dbsqlc.ModelModel, error)
	CreateModelVariant(context.Context, dbsqlc.CreateModelVariantParams) (dbsqlc.ModelModelVariant, error)
	CountModels(context.Context) (int64, error)
	CountModelsByType(context.Context, string) (int64, error)
	DeleteModel(context.Context, pgtype.UUID) error
	DeleteModelByProviderAndType(context.Context, dbsqlc.DeleteModelByProviderAndTypeParams) error
	GetModelByID(context.Context, pgtype.UUID) (dbsqlc.ModelModel, error)
	GetModelByProviderAndModelID(context.Context, dbsqlc.GetModelByProviderAndModelIDParams) (dbsqlc.ModelModel, error)
	ListEnabledModels(context.Context) ([]dbsqlc.ModelModel, error)
	ListEnabledModelsByProviderClientType(context.Context, string) ([]dbsqlc.ModelModel, error)
	ListEnabledModelsByType(context.Context, string) ([]dbsqlc.ModelModel, error)
	ListModelVariantsByModelUUID(context.Context, pgtype.UUID) ([]dbsqlc.ModelModelVariant, error)
	ListModels(context.Context) ([]dbsqlc.ModelModel, error)
	ListModelsByModelID(context.Context, string) ([]dbsqlc.ModelModel, error)
	ListModelsByProviderClientType(context.Context, string) ([]dbsqlc.ModelModel, error)
	ListModelsByProviderID(context.Context, pgtype.UUID) ([]dbsqlc.ModelModel, error)
	ListModelsByProviderIDAndType(context.Context, dbsqlc.ListModelsByProviderIDAndTypeParams) ([]dbsqlc.ModelModel, error)
	ListModelsByType(context.Context, string) ([]dbsqlc.ModelModel, error)
	UpdateModel(context.Context, dbsqlc.UpdateModelParams) (dbsqlc.ModelModel, error)
	UpsertRegistryModel(context.Context, dbsqlc.UpsertRegistryModelParams) (dbsqlc.ModelModel, error)
}

// Store adapts the current generated SQLC package to Models' persistence
// ports. It is intentionally transitional until owner-local SQLC is generated.
type Store struct {
	queries modelQueries
}

var _ catalogport.Store = (*Store)(nil)

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{queries: dbsqlc.New(pool)}
}

// NewStoreWithQueries creates a store with an injected query surface (tests).
func NewStoreWithQueries(q modelQueries) *Store {
	return &Store{queries: q}
}

func (s *Store) Create(ctx context.Context, input catalogport.CreateInput) (catalogport.Record, error) {
	providerID, err := db.ParseUUID(input.ProviderID)
	if err != nil {
		return catalogport.Record{}, err
	}
	row, err := s.queries.CreateModel(ctx, dbsqlc.CreateModelParams{
		ModelID:    input.ModelID,
		Name:       optionalText(input.Name),
		ProviderID: providerID,
		Type:       string(input.Type),
		Enable:     input.Enable,
		Config:     cloneBytes(input.Config),
	})
	if db.IsUniqueViolation(err) {
		return catalogport.Record{}, catalogport.ErrModelIDAlreadyExists
	}
	if err != nil {
		return catalogport.Record{}, err
	}
	return record(row), nil
}

func (s *Store) GetByID(ctx context.Context, id string) (catalogport.Record, error) {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return catalogport.Record{}, err
	}
	row, err := s.queries.GetModelByID(ctx, parsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return catalogport.Record{}, catalogport.ErrModelNotFound
	}
	if err != nil {
		return catalogport.Record{}, err
	}
	return record(row), nil
}

func (s *Store) GetByModelID(ctx context.Context, modelID string) (catalogport.Record, error) {
	rows, err := s.queries.ListModelsByModelID(ctx, modelID)
	if err != nil {
		return catalogport.Record{}, err
	}
	if len(rows) == 0 {
		return catalogport.Record{}, catalogport.ErrModelNotFound
	}
	if len(rows) > 1 {
		return catalogport.Record{}, catalogport.ErrModelIDAmbiguous
	}
	return record(rows[0]), nil
}

func (s *Store) GetByProviderAndModelID(ctx context.Context, providerID, modelID string) (catalogport.Record, error) {
	parsed, err := db.ParseUUID(providerID)
	if err != nil {
		return catalogport.Record{}, err
	}
	row, err := s.queries.GetModelByProviderAndModelID(ctx, dbsqlc.GetModelByProviderAndModelIDParams{
		ProviderID: parsed,
		ModelID:    modelID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return catalogport.Record{}, catalogport.ErrModelNotFound
	}
	if err != nil {
		return catalogport.Record{}, err
	}
	return record(row), nil
}

func (s *Store) List(ctx context.Context) ([]catalogport.Record, error) {
	rows, err := s.queries.ListModels(ctx)
	return records(rows), err
}

func (s *Store) ListByType(ctx context.Context, modelType modeldomain.ModelType) ([]catalogport.Record, error) {
	rows, err := s.queries.ListModelsByType(ctx, string(modelType))
	return records(rows), err
}

func (s *Store) ListByProviderID(ctx context.Context, providerID string) ([]catalogport.Record, error) {
	parsed, err := db.ParseUUID(providerID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListModelsByProviderID(ctx, parsed)
	return records(rows), err
}

func (s *Store) ListByProviderIDAndType(ctx context.Context, providerID string, modelType modeldomain.ModelType) ([]catalogport.Record, error) {
	parsed, err := db.ParseUUID(providerID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListModelsByProviderIDAndType(ctx, dbsqlc.ListModelsByProviderIDAndTypeParams{
		ProviderID: parsed,
		Type:       string(modelType),
	})
	return records(rows), err
}

func (s *Store) ListByProviderClientType(ctx context.Context, clientType modeldomain.ClientType) ([]catalogport.Record, error) {
	rows, err := s.queries.ListModelsByProviderClientType(ctx, string(clientType))
	return records(rows), err
}

func (s *Store) ListEnabled(ctx context.Context) ([]catalogport.Record, error) {
	rows, err := s.queries.ListEnabledModels(ctx)
	return records(rows), err
}

func (s *Store) ListEnabledByType(ctx context.Context, modelType modeldomain.ModelType) ([]catalogport.Record, error) {
	rows, err := s.queries.ListEnabledModelsByType(ctx, string(modelType))
	return records(rows), err
}

func (s *Store) ListEnabledByProviderClientType(ctx context.Context, clientType modeldomain.ClientType) ([]catalogport.Record, error) {
	rows, err := s.queries.ListEnabledModelsByProviderClientType(ctx, string(clientType))
	return records(rows), err
}

func (s *Store) Update(ctx context.Context, input catalogport.UpdateInput) (catalogport.Record, error) {
	id, err := db.ParseUUID(input.ID)
	if err != nil {
		return catalogport.Record{}, err
	}
	providerID, err := db.ParseUUID(input.ProviderID)
	if err != nil {
		return catalogport.Record{}, err
	}
	row, err := s.queries.UpdateModel(ctx, dbsqlc.UpdateModelParams{
		ID:         id,
		ModelID:    input.ModelID,
		Name:       optionalText(input.Name),
		ProviderID: providerID,
		Type:       string(input.Type),
		Enable:     input.Enable,
		Config:     cloneBytes(input.Config),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return catalogport.Record{}, catalogport.ErrModelNotFound
	}
	if db.IsUniqueViolation(err) {
		return catalogport.Record{}, catalogport.ErrModelIDAlreadyExists
	}
	if err != nil {
		return catalogport.Record{}, err
	}
	return record(row), nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return s.queries.DeleteModel(ctx, parsed)
}

func (s *Store) Count(ctx context.Context) (int64, error) {
	return s.queries.CountModels(ctx)
}

func (s *Store) CountByType(ctx context.Context, modelType modeldomain.ModelType) (int64, error) {
	return s.queries.CountModelsByType(ctx, string(modelType))
}

func (s *Store) CreateVariant(ctx context.Context, input catalogport.CreateVariantInput) (catalogport.VariantRecord, error) {
	modelID, err := db.ParseUUID(input.ModelID)
	if err != nil {
		return catalogport.VariantRecord{}, err
	}
	row, err := s.queries.CreateModelVariant(ctx, dbsqlc.CreateModelVariantParams{
		ModelUuid: modelID,
		VariantID: input.VariantID,
		Weight:    input.Weight,
		Metadata:  cloneBytes(input.Metadata),
	})
	if err != nil {
		return catalogport.VariantRecord{}, err
	}
	return variantRecord(row), nil
}

func (s *Store) ListVariants(ctx context.Context, modelID string) ([]catalogport.VariantRecord, error) {
	parsed, err := db.ParseUUID(modelID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListModelVariantsByModelUUID(ctx, parsed)
	if err != nil {
		return nil, err
	}
	items := make([]catalogport.VariantRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, variantRecord(row))
	}
	return items, nil
}

func (s *Store) UpsertCatalogModel(ctx context.Context, input catalogport.CatalogModelInput) (catalogport.Record, error) {
	providerID, err := db.ParseUUID(input.ProviderID)
	if err != nil {
		return catalogport.Record{}, err
	}
	row, err := s.queries.UpsertRegistryModel(ctx, dbsqlc.UpsertRegistryModelParams{
		ModelID:    input.ModelID,
		Name:       optionalText(input.Name),
		ProviderID: providerID,
		Type:       string(input.Type),
		Config:     cloneBytes(input.Config),
	})
	if err != nil {
		return catalogport.Record{}, err
	}
	return record(row), nil
}

func (s *Store) DeleteCatalogModel(ctx context.Context, providerID, modelID string, modelType modeldomain.ModelType) error {
	parsed, err := db.ParseUUID(providerID)
	if err != nil {
		return err
	}
	return s.queries.DeleteModelByProviderAndType(ctx, dbsqlc.DeleteModelByProviderAndTypeParams{
		ProviderID: parsed,
		ModelID:    modelID,
		Type:       string(modelType),
	})
}

func record(row dbsqlc.ModelModel) catalogport.Record {
	return catalogport.Record{
		ID:         db.UUIDString(row.ID),
		ModelID:    row.ModelID,
		Name:       db.TextToString(row.Name),
		ProviderID: db.UUIDString(row.ProviderID),
		Type:       modeldomain.ModelType(row.Type),
		Enable:     row.Enable,
		Config:     cloneBytes(row.Config),
	}
}

func records(rows []dbsqlc.ModelModel) []catalogport.Record {
	items := make([]catalogport.Record, 0, len(rows))
	for _, row := range rows {
		items = append(items, record(row))
	}
	return items
}

func variantRecord(row dbsqlc.ModelModelVariant) catalogport.VariantRecord {
	return catalogport.VariantRecord{
		ID:        db.UUIDString(row.ID),
		ModelID:   db.UUIDString(row.ModelUuid),
		VariantID: row.VariantID,
		Weight:    row.Weight,
		Metadata:  cloneBytes(row.Metadata),
	}
}

func optionalText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
