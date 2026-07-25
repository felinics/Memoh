package audio

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"testing"

	modeldomain "github.com/memohai/memoh/domains/model"
	audioport "github.com/memohai/memoh/domains/model/internal/port/audio"
)

type serviceStoreStub struct {
	audioport.Store
	getSpeechModel func(context.Context, string) (audioport.ModelRecord, error)
	updateModel    func(context.Context, audioport.UpdateModelInput) (audioport.ModelRecord, error)
}

func (s *serviceStoreStub) GetSpeechModel(ctx context.Context, id string) (audioport.ModelRecord, error) {
	return s.getSpeechModel(ctx, id)
}

func (s *serviceStoreStub) UpdateAudioModel(ctx context.Context, input audioport.UpdateModelInput) (audioport.ModelRecord, error) {
	return s.updateModel(ctx, input)
}

func TestUpdateSpeechModelPreservesPersistedIdentity(t *testing.T) {
	const (
		modelID    = "11111111-1111-1111-1111-111111111111"
		providerID = "22222222-2222-2222-2222-222222222222"
	)

	name := "Renamed voice"
	wantConfig := map[string]any{"voice": "alloy", "speed": 1.25}
	store := &serviceStoreStub{}
	store.getSpeechModel = func(_ context.Context, id string) (audioport.ModelRecord, error) {
		if id != modelID {
			t.Fatalf("GetSpeechModel id = %q, want %q", id, modelID)
		}
		return audioport.ModelRecord{
			ID:           modelID,
			ModelID:      "tts-1",
			Name:         "Old voice",
			ProviderID:   providerID,
			ProviderType: string(modeldomain.ClientTypeOpenAISpeech),
			Type:         modeldomain.ModelTypeSpeech,
			Enable:       true,
			Config:       json.RawMessage(`{"voice":"echo"}`),
		}, nil
	}
	store.updateModel = func(_ context.Context, input audioport.UpdateModelInput) (audioport.ModelRecord, error) {
		if input.ID != modelID || input.ModelID != "tts-1" || input.ProviderID != providerID {
			t.Fatalf("UpdateModel identity = %#v", input)
		}
		if input.Name != name || input.Type != modeldomain.ModelTypeSpeech || !input.Enable {
			t.Fatalf("UpdateModel preserved state = %#v", input)
		}
		var config map[string]any
		if err := json.Unmarshal(input.Config, &config); err != nil {
			t.Fatalf("unmarshal UpdateModel config: %v", err)
		}
		if !reflect.DeepEqual(config, wantConfig) {
			t.Fatalf("UpdateModel config = %#v, want %#v", config, wantConfig)
		}
		return audioport.ModelRecord{
			ID:         input.ID,
			ModelID:    input.ModelID,
			Name:       input.Name,
			ProviderID: input.ProviderID,
			Type:       input.Type,
			Enable:     input.Enable,
			Config:     input.Config,
		}, nil
	}

	service := NewService(slog.New(slog.DiscardHandler), store, &Registry{})
	got, err := service.UpdateSpeechModel(t.Context(), modelID, UpdateSpeechModelRequest{
		Name:   &name,
		Config: wantConfig,
	})
	if err != nil {
		t.Fatalf("UpdateSpeechModel() error = %v", err)
	}
	if got.ProviderType != string(modeldomain.ClientTypeOpenAISpeech) {
		t.Fatalf("ProviderType = %q", got.ProviderType)
	}
	if got.Name != name || !reflect.DeepEqual(got.Config, wantConfig) {
		t.Fatalf("response = %#v", got)
	}
}

type catalogStoreStub struct {
	audioport.CatalogStore
	provider audioport.ProviderRecord
	upserts  []audioport.CatalogModelInput
	deletes  []audioport.CatalogModelInput
}

func (s *catalogStoreStub) GetProviderByClientType(_ context.Context, _ modeldomain.ClientType) (audioport.ProviderRecord, error) {
	return s.provider, nil
}

func (s *catalogStoreStub) UpsertAudioCatalogModel(_ context.Context, input audioport.CatalogModelInput) (audioport.ModelRecord, error) {
	s.upserts = append(s.upserts, input)
	return audioport.ModelRecord{}, nil
}

func (s *catalogStoreStub) DeleteAudioCatalogModel(_ context.Context, providerID, modelID string, modelType modeldomain.ModelType) error {
	s.deletes = append(s.deletes, audioport.CatalogModelInput{ProviderID: providerID, ModelID: modelID, Type: modelType})
	return nil
}

func TestSyncRegistryUsesCatalogPort(t *testing.T) {
	registry := &Registry{providers: make(map[modeldomain.ClientType]ProviderDefinition)}
	registry.Register(ProviderDefinition{
		ClientType:   modeldomain.ClientTypeOpenAISpeech,
		DisplayName:  "OpenAI Speech",
		SupportsList: true,
		Models: []ModelInfo{
			{ID: "template-only", Name: "Template", TemplateOnly: true},
			{ID: "tts-1", Name: "Speech"},
		},
		TranscriptionModels: []ModelInfo{{ID: "stt-1", Name: "Transcription"}},
	})
	store := &catalogStoreStub{provider: audioport.ProviderRecord{ID: "provider-1"}}

	if err := SyncRegistry(t.Context(), slog.New(slog.DiscardHandler), store, registry); err != nil {
		t.Fatalf("SyncRegistry() error = %v", err)
	}
	if len(store.deletes) != 1 || store.deletes[0].ModelID != "template-only" || store.deletes[0].Type != modeldomain.ModelTypeSpeech {
		t.Fatalf("deletes = %#v", store.deletes)
	}
	if len(store.upserts) != 2 {
		t.Fatalf("upserts = %#v", store.upserts)
	}
	if store.upserts[0].ModelID != "tts-1" || store.upserts[0].Type != modeldomain.ModelTypeSpeech {
		t.Fatalf("speech upsert = %#v", store.upserts[0])
	}
	if store.upserts[1].ModelID != "stt-1" || store.upserts[1].Type != modeldomain.ModelTypeTranscription {
		t.Fatalf("transcription upsert = %#v", store.upserts[1])
	}
	for _, input := range store.upserts {
		if string(input.Config) != "{}" {
			t.Fatalf("catalog config = %s", input.Config)
		}
	}
}
