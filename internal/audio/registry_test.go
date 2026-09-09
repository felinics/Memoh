package audio

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/felinics/memoh/internal/models"
)

func TestAlibabaTranscriptionRegistry(t *testing.T) {
	r := NewRegistry()
	def, err := r.Get(models.ClientTypeAlibabaTranscription)
	require.NoError(t, err)
	require.Nil(t, def.Factory)
	require.NotNil(t, def.TranscriptionFactory)
	require.True(t, models.IsValidClientType(def.ClientType))
	require.False(t, models.IsLLMClientType(def.ClientType))
	p, err := def.TranscriptionFactory(map[string]any{"api_key": "test-key"})
	require.NoError(t, err)
	catalog, err := p.ListModels(t.Context())
	require.NoError(t, err)
	require.Len(t, catalog, 1)
	require.Equal(t, def.DefaultTranscriptionModel, catalog[0].ID)
	var transcriptionTypes []string
	for _, meta := range r.ListTranscriptionMeta() {
		transcriptionTypes = append(transcriptionTypes, meta.Provider)
	}
	require.Contains(t, transcriptionTypes, string(def.ClientType))
	for _, meta := range r.ListSpeechMeta() {
		require.NotEqual(t, string(def.ClientType), meta.Provider)
	}
	// Dedicated ASR registration must not change existing speech-derived registrations.
	require.Contains(t, transcriptionTypes, string(models.ClientTypeOpenAITranscription))
	require.Contains(t, transcriptionTypes, string(models.ClientTypeGoogleTranscription))
}
