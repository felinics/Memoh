package audio

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	modeldomain "github.com/memohai/memoh/domains/model"
	audioport "github.com/memohai/memoh/domains/model/internal/port/audio"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
)

type audioQueryStub struct {
	legacyQueries
	getSpeechModel func(context.Context, pgtype.UUID) (dbsqlc.GetSpeechModelWithProviderRow, error)
	updateModel    func(context.Context, dbsqlc.UpdateModelParams) (dbsqlc.ModelModel, error)
	providerErr    error
}

func (s *audioQueryStub) GetSpeechModelWithProvider(ctx context.Context, id pgtype.UUID) (dbsqlc.GetSpeechModelWithProviderRow, error) {
	return s.getSpeechModel(ctx, id)
}

func (s *audioQueryStub) UpdateModel(ctx context.Context, input dbsqlc.UpdateModelParams) (dbsqlc.ModelModel, error) {
	return s.updateModel(ctx, input)
}

func (s *audioQueryStub) GetProviderByClientType(context.Context, string) (dbsqlc.ModelProvider, error) {
	return dbsqlc.ModelProvider{}, s.providerErr
}

func TestAudioStoreMapsGeneratedRows(t *testing.T) {
	modelID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	providerID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	createdAt := time.Date(2026, time.July, 23, 1, 2, 3, 0, time.UTC)
	updatedAt := createdAt.Add(time.Hour)
	rawConfig := []byte(`{"voice":"alloy"}`)
	queries := &audioQueryStub{}
	queries.getSpeechModel = func(_ context.Context, got pgtype.UUID) (dbsqlc.GetSpeechModelWithProviderRow, error) {
		if uuid.UUID(got.Bytes) != modelID {
			t.Fatalf("model id = %s", uuid.UUID(got.Bytes))
		}
		return dbsqlc.GetSpeechModelWithProviderRow{
			ID: pgUUID(modelID), ModelID: "tts-1", Name: pgtype.Text{String: "Speech", Valid: true},
			ProviderID: pgUUID(providerID), Type: string(modeldomain.ModelTypeSpeech), Enable: true, Config: rawConfig,
			CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true}, UpdatedAt: pgtype.Timestamptz{Time: updatedAt, Valid: true},
			ProviderType: string(modeldomain.ClientTypeOpenAISpeech),
		}, nil
	}

	record, err := NewStoreWithQueries(queries).GetSpeechModel(t.Context(), modelID.String())
	if err != nil {
		t.Fatalf("GetSpeechModel() error = %v", err)
	}
	if record.ID != modelID.String() || record.ProviderID != providerID.String() || record.Name != "Speech" {
		t.Fatalf("record = %#v", record)
	}
	if record.Type != modeldomain.ModelTypeSpeech || record.ProviderType != string(modeldomain.ClientTypeOpenAISpeech) || !record.Enable {
		t.Fatalf("record type state = %#v", record)
	}
	rawConfig[0] = '['
	if string(record.Config) != `{"voice":"alloy"}` {
		t.Fatalf("record config aliases generated row: %s", record.Config)
	}
}

func TestAudioStoreMapsInputsAndClassifiesMissingRows(t *testing.T) {
	modelID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	providerID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	input := audioport.UpdateModelInput{
		ID: modelID.String(), ModelID: "tts-1", Name: "Updated", ProviderID: providerID.String(),
		Type: modeldomain.ModelTypeSpeech, Enable: true, Config: json.RawMessage(`{"speed":1.2}`),
	}
	queries := &audioQueryStub{}
	queries.updateModel = func(_ context.Context, got dbsqlc.UpdateModelParams) (dbsqlc.ModelModel, error) {
		if uuid.UUID(got.ID.Bytes) != modelID || uuid.UUID(got.ProviderID.Bytes) != providerID {
			t.Fatalf("UpdateAudioModel UUIDs = %#v", got)
		}
		return dbsqlc.ModelModel{
			ID: got.ID, ModelID: got.ModelID, Name: got.Name, ProviderID: got.ProviderID,
			Type: got.Type, Enable: got.Enable, Config: got.Config,
		}, nil
	}

	record, err := NewStoreWithQueries(queries).UpdateAudioModel(t.Context(), input)
	if err != nil {
		t.Fatalf("UpdateAudioModel() error = %v", err)
	}
	if record.ID != input.ID || record.ProviderID != input.ProviderID || record.Name != input.Name {
		t.Fatalf("record = %#v", record)
	}

	queries.providerErr = pgx.ErrNoRows
	_, err = NewStoreWithQueries(queries).GetProviderByClientType(t.Context(), modeldomain.ClientTypeOpenAISpeech)
	if !errors.Is(err, audioport.ErrProviderNotFound) {
		t.Fatalf("GetProviderByClientType() error = %v, want ErrProviderNotFound", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetProviderByClientType() leaked pgx.ErrNoRows: %v", err)
	}
}

func pgUUID(value uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte(value), Valid: true}
}
