package audio

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	modeldomain "github.com/memohai/memoh/domains/model"
	audioport "github.com/memohai/memoh/domains/model/internal/port/audio"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type legacyQueries interface {
	DeleteModelByProviderAndType(context.Context, dbsqlc.DeleteModelByProviderAndTypeParams) error
	GetProviderByClientType(context.Context, string) (dbsqlc.ModelProvider, error)
	GetProviderByID(context.Context, pgtype.UUID) (dbsqlc.ModelProvider, error)
	GetSpeechModelWithProvider(context.Context, pgtype.UUID) (dbsqlc.GetSpeechModelWithProviderRow, error)
	GetTranscriptionModelWithProvider(context.Context, pgtype.UUID) (dbsqlc.GetTranscriptionModelWithProviderRow, error)
	ListSpeechModels(context.Context) ([]dbsqlc.ListSpeechModelsRow, error)
	ListSpeechModelsByProviderID(context.Context, pgtype.UUID) ([]dbsqlc.ModelModel, error)
	ListSpeechProviders(context.Context) ([]dbsqlc.ModelProvider, error)
	ListTranscriptionModels(context.Context) ([]dbsqlc.ListTranscriptionModelsRow, error)
	ListTranscriptionModelsByProviderID(context.Context, pgtype.UUID) ([]dbsqlc.ModelModel, error)
	ListTranscriptionProviders(context.Context) ([]dbsqlc.ModelProvider, error)
	UpdateModel(context.Context, dbsqlc.UpdateModelParams) (dbsqlc.ModelModel, error)
	UpsertRegistryModel(context.Context, dbsqlc.UpsertRegistryModelParams) (dbsqlc.ModelModel, error)
}

// Store adapts the current generated statements to the audio persistence port.
type Store struct {
	queries legacyQueries
}

var (
	_ audioport.Store        = (*Store)(nil)
	_ audioport.CatalogStore = (*Store)(nil)
)

// NewStore creates a postgres-backed audio store from a connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{queries: dbsqlc.New(pool)}
}

// NewStoreWithQueries creates a store with an injected query surface (tests).
func NewStoreWithQueries(queries legacyQueries) *Store {
	return &Store{queries: queries}
}

func (s *Store) ListSpeechProviders(ctx context.Context) ([]audioport.ProviderRecord, error) {
	rows, err := s.queries.ListSpeechProviders(ctx)
	return providerRecords(rows), err
}

func (s *Store) ListTranscriptionProviders(ctx context.Context) ([]audioport.ProviderRecord, error) {
	rows, err := s.queries.ListTranscriptionProviders(ctx)
	return providerRecords(rows), err
}

func (s *Store) GetProvider(ctx context.Context, id string) (audioport.ProviderRecord, error) {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return audioport.ProviderRecord{}, err
	}
	row, err := s.queries.GetProviderByID(ctx, parsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return audioport.ProviderRecord{}, audioport.ErrProviderNotFound
	}
	if err != nil {
		return audioport.ProviderRecord{}, err
	}
	return providerRecord(row), nil
}

func (s *Store) GetProviderByClientType(ctx context.Context, clientType modeldomain.ClientType) (audioport.ProviderRecord, error) {
	row, err := s.queries.GetProviderByClientType(ctx, string(clientType))
	if errors.Is(err, pgx.ErrNoRows) {
		return audioport.ProviderRecord{}, audioport.ErrProviderNotFound
	}
	if err != nil {
		return audioport.ProviderRecord{}, err
	}
	return providerRecord(row), nil
}

func (s *Store) ListSpeechModels(ctx context.Context) ([]audioport.ModelRecord, error) {
	rows, err := s.queries.ListSpeechModels(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]audioport.ModelRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, speechModelRecordFromList(row))
	}
	return items, nil
}

func (s *Store) ListTranscriptionModels(ctx context.Context) ([]audioport.ModelRecord, error) {
	rows, err := s.queries.ListTranscriptionModels(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]audioport.ModelRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, transcriptionModelRecordFromList(row))
	}
	return items, nil
}

func (s *Store) ListSpeechModelsByProvider(ctx context.Context, providerID string) ([]audioport.ModelRecord, error) {
	parsed, err := db.ParseUUID(providerID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListSpeechModelsByProviderID(ctx, parsed)
	return modelRecords(rows), err
}

func (s *Store) ListTranscriptionModelsByProvider(ctx context.Context, providerID string) ([]audioport.ModelRecord, error) {
	parsed, err := db.ParseUUID(providerID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListTranscriptionModelsByProviderID(ctx, parsed)
	return modelRecords(rows), err
}

func (s *Store) GetSpeechModel(ctx context.Context, id string) (audioport.ModelRecord, error) {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return audioport.ModelRecord{}, err
	}
	row, err := s.queries.GetSpeechModelWithProvider(ctx, parsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return audioport.ModelRecord{}, audioport.ErrModelNotFound
	}
	if err != nil {
		return audioport.ModelRecord{}, err
	}
	return speechModelRecord(row), nil
}

func (s *Store) GetTranscriptionModel(ctx context.Context, id string) (audioport.ModelRecord, error) {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return audioport.ModelRecord{}, err
	}
	row, err := s.queries.GetTranscriptionModelWithProvider(ctx, parsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return audioport.ModelRecord{}, audioport.ErrModelNotFound
	}
	if err != nil {
		return audioport.ModelRecord{}, err
	}
	return transcriptionModelRecord(row), nil
}

func (s *Store) UpdateAudioModel(ctx context.Context, input audioport.UpdateModelInput) (audioport.ModelRecord, error) {
	id, err := db.ParseUUID(input.ID)
	if err != nil {
		return audioport.ModelRecord{}, err
	}
	providerID, err := db.ParseUUID(input.ProviderID)
	if err != nil {
		return audioport.ModelRecord{}, err
	}
	row, err := s.queries.UpdateModel(ctx, dbsqlc.UpdateModelParams{
		ID: id, ModelID: input.ModelID, Name: optionalText(input.Name), ProviderID: providerID,
		Type: string(input.Type), Enable: input.Enable, Config: cloneBytes(input.Config),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return audioport.ModelRecord{}, audioport.ErrModelNotFound
	}
	if err != nil {
		return audioport.ModelRecord{}, err
	}
	return modelRecord(row), nil
}

func (s *Store) UpsertAudioCatalogModel(ctx context.Context, input audioport.CatalogModelInput) (audioport.ModelRecord, error) {
	providerID, err := db.ParseUUID(input.ProviderID)
	if err != nil {
		return audioport.ModelRecord{}, err
	}
	row, err := s.queries.UpsertRegistryModel(ctx, dbsqlc.UpsertRegistryModelParams{
		ModelID: input.ModelID, Name: optionalText(input.Name), ProviderID: providerID,
		Type: string(input.Type), Config: cloneBytes(input.Config),
	})
	if err != nil {
		return audioport.ModelRecord{}, err
	}
	return modelRecord(row), nil
}

func (s *Store) DeleteAudioCatalogModel(ctx context.Context, providerID, modelID string, modelType modeldomain.ModelType) error {
	parsed, err := db.ParseUUID(providerID)
	if err != nil {
		return err
	}
	return s.queries.DeleteModelByProviderAndType(ctx, dbsqlc.DeleteModelByProviderAndTypeParams{
		ProviderID: parsed, ModelID: modelID, Type: string(modelType),
	})
}

func providerRecords(rows []dbsqlc.ModelProvider) []audioport.ProviderRecord {
	items := make([]audioport.ProviderRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, providerRecord(row))
	}
	return items
}

func providerRecord(row dbsqlc.ModelProvider) audioport.ProviderRecord {
	return audioport.ProviderRecord{
		ID: db.UUIDString(row.ID), Name: row.Name, ClientType: row.ClientType, Icon: db.TextToString(row.Icon),
		Enable: row.Enable, Config: cloneBytes(row.Config), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func modelRecords(rows []dbsqlc.ModelModel) []audioport.ModelRecord {
	items := make([]audioport.ModelRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, modelRecord(row))
	}
	return items
}

func modelRecord(row dbsqlc.ModelModel) audioport.ModelRecord {
	return audioport.ModelRecord{
		ID: db.UUIDString(row.ID), ModelID: row.ModelID, Name: db.TextToString(row.Name), ProviderID: db.UUIDString(row.ProviderID),
		Type: modeldomain.ModelType(row.Type), Enable: row.Enable, Config: cloneBytes(row.Config),
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func speechModelRecord(row dbsqlc.GetSpeechModelWithProviderRow) audioport.ModelRecord {
	return audioport.ModelRecord{
		ID: db.UUIDString(row.ID), ModelID: row.ModelID, Name: db.TextToString(row.Name), ProviderID: db.UUIDString(row.ProviderID),
		ProviderType: row.ProviderType, Type: modeldomain.ModelType(row.Type), Enable: row.Enable, Config: cloneBytes(row.Config),
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func transcriptionModelRecord(row dbsqlc.GetTranscriptionModelWithProviderRow) audioport.ModelRecord {
	return audioport.ModelRecord{
		ID: db.UUIDString(row.ID), ModelID: row.ModelID, Name: db.TextToString(row.Name), ProviderID: db.UUIDString(row.ProviderID),
		ProviderType: row.ProviderType, Type: modeldomain.ModelType(row.Type), Enable: row.Enable, Config: cloneBytes(row.Config),
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func speechModelRecordFromList(row dbsqlc.ListSpeechModelsRow) audioport.ModelRecord {
	return audioport.ModelRecord{
		ID: db.UUIDString(row.ID), ModelID: row.ModelID, Name: db.TextToString(row.Name), ProviderID: db.UUIDString(row.ProviderID),
		ProviderType: row.ProviderType, Type: modeldomain.ModelType(row.Type), Enable: row.Enable, Config: cloneBytes(row.Config),
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func transcriptionModelRecordFromList(row dbsqlc.ListTranscriptionModelsRow) audioport.ModelRecord {
	return audioport.ModelRecord{
		ID: db.UUIDString(row.ID), ModelID: row.ModelID, Name: db.TextToString(row.Name), ProviderID: db.UUIDString(row.ProviderID),
		ProviderType: row.ProviderType, Type: modeldomain.ModelType(row.Type), Enable: row.Enable, Config: cloneBytes(row.Config),
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
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
