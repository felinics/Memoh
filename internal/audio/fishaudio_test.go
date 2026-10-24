package audio

import (
	"context"
	"slices"
	"testing"

	"github.com/felinics/memoh/internal/models"
)

func TestFishAudioRegistrySeparatesSpeechAndTranscription(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	for _, tt := range []struct {
		clientType string
		wantIDs    []string
		defaultID  string
		isSTT      bool
	}{
		{"fishaudio-speech", []string{"s2.1-pro", "s2.1-pro-free", "drama-3"}, "s2.1-pro", false},
		{"fishaudio-transcription", []string{"transcribe-1-pro", "transcribe-1"}, "transcribe-1-pro", true},
	} {
		t.Run(tt.clientType, func(t *testing.T) {
			clientType := models.ClientType(tt.clientType)
			if !models.IsValidClientType(clientType) || models.IsLLMClientType(clientType) {
				t.Fatalf("%s must be a supported audio-only client type", clientType)
			}
			def, err := registry.Get(clientType)
			if err != nil {
				t.Fatal(err)
			}
			var gotIDs []string
			if tt.isSTT {
				if def.Factory != nil || def.TranscriptionFactory == nil {
					t.Fatal("transcription entry must only create a transcription provider")
				}
				provider, err := def.TranscriptionFactory(map[string]any{"api_key": "test-key"})
				if err != nil {
					t.Fatal(err)
				}
				catalog, err := provider.ListModels(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				for _, model := range catalog {
					gotIDs = append(gotIDs, model.ID)
				}
				if def.DefaultTranscriptionModel != tt.defaultID {
					t.Fatalf("default transcription model = %q", def.DefaultTranscriptionModel)
				}
			} else {
				if def.Factory == nil {
					t.Fatal("speech entry must create a speech provider")
				}
				provider, err := def.Factory(map[string]any{"api_key": "test-key"})
				if err != nil {
					t.Fatal(err)
				}
				catalog, err := provider.ListModels(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				for _, model := range catalog {
					gotIDs = append(gotIDs, model.ID)
				}
				if def.DefaultModel != tt.defaultID {
					t.Fatalf("default speech model = %q", def.DefaultModel)
				}
			}
			if !slices.Equal(gotIDs, tt.wantIDs) {
				t.Fatalf("importable model IDs = %v, want %v", gotIDs, tt.wantIDs)
			}
			metas := registry.ListSpeechMeta()
			other := registry.ListTranscriptionMeta()
			if tt.isSTT {
				metas, other = other, metas
			}
			if !slices.ContainsFunc(metas, func(meta ProviderMetaResponse) bool { return meta.Provider == tt.clientType }) {
				t.Fatal("provider missing from its audio catalog")
			}
			if slices.ContainsFunc(other, func(meta ProviderMetaResponse) bool { return meta.Provider == tt.clientType }) {
				t.Fatal("provider leaked into the opposite audio catalog")
			}
		})
	}
}
