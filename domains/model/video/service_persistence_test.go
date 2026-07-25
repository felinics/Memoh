package video

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	modeldomain "github.com/memohai/memoh/domains/model"
	videoport "github.com/memohai/memoh/domains/model/internal/port/video"
)

type serviceStoreStub struct {
	videoport.Store
	model      videoport.ModelRecord
	getModelID string
	update     videoport.UpdateModelInput
}

func (s *serviceStoreStub) GetModel(_ context.Context, id string) (videoport.ModelRecord, error) {
	s.getModelID = id
	return s.model, nil
}

func (s *serviceStoreStub) UpdateVideoModel(_ context.Context, input videoport.UpdateModelInput) (videoport.ModelRecord, error) {
	s.update = input
	return videoport.ModelRecord{
		ID:         input.ID,
		ModelID:    input.ModelID,
		Name:       input.Name,
		ProviderID: input.ProviderID,
		Type:       input.Type,
		Enable:     input.Enable,
		Config:     append(json.RawMessage(nil), input.Config...),
	}, nil
}

func TestServiceUpdateModelUsesVideoStoreRecord(t *testing.T) {
	store := &serviceStoreStub{model: videoport.ModelRecord{
		ID:           "0f18ee98-a490-4f33-8ab1-34f8f243d9ee",
		ModelID:      "remote-model",
		Name:         "old name",
		ProviderID:   "69dc7e53-a282-45db-af34-c99fd232d78d",
		Type:         modeldomain.ModelTypeVideo,
		Enable:       true,
		ProviderType: modeldomain.ClientTypeOpenRouterVideo,
	}}
	service := NewService(nil, store, NewRegistry())
	name := "new name"

	got, err := service.UpdateModel(t.Context(), "0f18ee98-a490-4f33-8ab1-34f8f243d9ee", UpdateModelRequest{
		Name:   &name,
		Config: map[string]any{"duration": float64(5)},
	})
	if err != nil {
		t.Fatalf("UpdateModel() error = %v", err)
	}
	if store.getModelID != "0f18ee98-a490-4f33-8ab1-34f8f243d9ee" {
		t.Fatalf("GetModel() id = %q", store.getModelID)
	}
	if store.update.ID != "0f18ee98-a490-4f33-8ab1-34f8f243d9ee" || store.update.ModelID != "remote-model" || store.update.ProviderID != "69dc7e53-a282-45db-af34-c99fd232d78d" {
		t.Fatalf("UpdateModel() identity = %+v", store.update)
	}
	if store.update.Name != name || store.update.Type != modeldomain.ModelTypeVideo || !store.update.Enable {
		t.Fatalf("UpdateModel() state = %+v", store.update)
	}
	if got.ProviderType != string(modeldomain.ClientTypeOpenRouterVideo) {
		t.Fatalf("response provider type = %q, want %q", got.ProviderType, modeldomain.ClientTypeOpenRouterVideo)
	}
	if got.Config["duration"] != float64(5) {
		t.Fatalf("response config = %#v", got.Config)
	}
}

func TestServiceRejectsInvalidModelIDBeforeStoreCall(t *testing.T) {
	store := &serviceStoreStub{}
	service := NewService(nil, store, NewRegistry())

	_, err := service.GetModel(t.Context(), "not-a-uuid")
	if err == nil || !strings.HasPrefix(err.Error(), "invalid UUID:") {
		t.Fatalf("GetModel() error = %v, want unwrapped invalid UUID", err)
	}
	if store.getModelID != "" {
		t.Fatalf("GetModel() called with %q for invalid ID", store.getModelID)
	}
}

type catalogStoreStub struct {
	providerType modeldomain.ClientType
	provider     videoport.ProviderRecord
	upserts      []videoport.CatalogModelInput
}

func (s *catalogStoreStub) GetProviderByClientType(_ context.Context, clientType modeldomain.ClientType) (videoport.ProviderRecord, error) {
	if clientType != s.providerType {
		return videoport.ProviderRecord{}, videoport.ErrProviderNotFound
	}
	return s.provider, nil
}

func (s *catalogStoreStub) UpsertVideoCatalogModel(_ context.Context, model videoport.CatalogModelInput) error {
	s.upserts = append(s.upserts, model)
	return nil
}

func TestSyncRegistryUsesCatalogStoreAndSkipsMissingProviders(t *testing.T) {
	const clientType modeldomain.ClientType = "test-video"
	registry := &Registry{
		providers: map[modeldomain.ClientType]ProviderDefinition{
			"missing-video": {ClientType: "missing-video", Models: []ModelInfo{{ID: "ignored"}}},
			clientType: {
				ClientType: clientType,
				Models:     []ModelInfo{{ID: "remote-model", Name: "Display Name"}},
			},
		},
		ordered: []modeldomain.ClientType{"missing-video", clientType},
	}
	store := &catalogStoreStub{
		providerType: clientType,
		provider:     videoport.ProviderRecord{ID: "provider-id", ClientType: clientType},
	}

	if err := SyncRegistry(t.Context(), nil, store, registry); err != nil {
		t.Fatalf("SyncRegistry() error = %v", err)
	}
	if len(store.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(store.upserts))
	}
	got := store.upserts[0]
	if got.ModelID != "remote-model" || got.Name != "Display Name" || got.ProviderID != "provider-id" || got.Type != modeldomain.ModelTypeVideo {
		t.Fatalf("upsert = %+v", got)
	}
	if !json.Valid(got.Config) {
		t.Fatalf("upsert config is not JSON: %q", got.Config)
	}
}

func TestSyncRegistryReturnsUnexpectedProviderLookupError(t *testing.T) {
	wantErr := errors.New("database unavailable")
	store := failingCatalogStore{err: wantErr}
	registry := &Registry{
		providers: map[modeldomain.ClientType]ProviderDefinition{
			"test-video": {ClientType: "test-video"},
		},
		ordered: []modeldomain.ClientType{"test-video"},
	}

	err := SyncRegistry(t.Context(), nil, store, registry)
	if !errors.Is(err, wantErr) {
		t.Fatalf("SyncRegistry() error = %v, want wrapping %v", err, wantErr)
	}
}

type failingCatalogStore struct {
	err error
}

func (s failingCatalogStore) GetProviderByClientType(context.Context, modeldomain.ClientType) (videoport.ProviderRecord, error) {
	return videoport.ProviderRecord{}, s.err
}

func (failingCatalogStore) UpsertVideoCatalogModel(context.Context, videoport.CatalogModelInput) error {
	return nil
}
