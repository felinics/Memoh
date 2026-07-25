package video

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	modeldomain "github.com/memohai/memoh/domains/model"
	videoport "github.com/memohai/memoh/domains/model/internal/port/video"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

const (
	videoModelID    = "0f18ee98-a490-4f33-8ab1-34f8f243d9ee"
	videoProviderID = "69dc7e53-a282-45db-af34-c99fd232d78d"
)

type videoQueryStub struct {
	legacyQueries
	providerErr  error
	updatedModel dbsqlc.ModelModel
	updateParams dbsqlc.UpdateModelParams
}

func (s *videoQueryStub) GetProviderByClientType(context.Context, string) (dbsqlc.ModelProvider, error) {
	return dbsqlc.ModelProvider{}, s.providerErr
}

func (s *videoQueryStub) UpdateModel(_ context.Context, params dbsqlc.UpdateModelParams) (dbsqlc.ModelModel, error) {
	s.updateParams = params
	return s.updatedModel, nil
}

func TestVideoStoreClassifiesMissingProvider(t *testing.T) {
	store := NewStoreWithQueries(&videoQueryStub{providerErr: pgx.ErrNoRows})

	_, err := store.GetProviderByClientType(t.Context(), modeldomain.ClientTypeOpenRouterVideo)
	if !errors.Is(err, videoport.ErrProviderNotFound) {
		t.Fatalf("GetProviderByClientType() error = %v, want ErrProviderNotFound", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetProviderByClientType() leaked pgx.ErrNoRows: %v", err)
	}
}

func TestVideoStoreMapsDomainInputAndGeneratedRow(t *testing.T) {
	modelUUID := mustUUID(t, videoModelID)
	providerUUID := mustUUID(t, videoProviderID)
	queries := &videoQueryStub{updatedModel: dbsqlc.ModelModel{
		ID: modelUUID, ModelID: "remote-model", Name: pgtype.Text{String: "Display Name", Valid: true},
		ProviderID: providerUUID, Type: string(modeldomain.ModelTypeVideo), Enable: true, Config: []byte(`{"duration":5}`),
	}}
	store := NewStoreWithQueries(queries)

	got, err := store.UpdateVideoModel(t.Context(), videoport.UpdateModelInput{
		ID: videoModelID, ModelID: "remote-model", Name: "Display Name", ProviderID: videoProviderID,
		Type: modeldomain.ModelTypeVideo, Enable: true, Config: []byte(`{"duration":5}`),
	})
	if err != nil {
		t.Fatalf("UpdateVideoModel() error = %v", err)
	}
	params := queries.updateParams
	if params.ID != modelUUID || params.ProviderID != providerUUID || params.ModelID != "remote-model" {
		t.Fatalf("generated params identity = %+v", params)
	}
	if got.ID != videoModelID || got.ProviderID != videoProviderID || got.Name != "Display Name" || !got.Enable {
		t.Fatalf("domain record = %+v", got)
	}
	params.Config[0] = 'x'
	if string(got.Config) != `{"duration":5}` {
		t.Fatalf("record config aliased generated params: %q", got.Config)
	}
}

func mustUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	id, err := db.ParseUUID(value)
	if err != nil {
		t.Fatalf("ParseUUID(%q): %v", value, err)
	}
	return id
}
