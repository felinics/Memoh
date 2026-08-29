package models_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/felinics/memoh/internal/models"
)

func intPtr(v int) *int { return &v }

func TestModel_Validate(t *testing.T) {
	tests := []struct {
		name    string
		model   models.Model
		wantErr bool
	}{
		{
			name: "valid chat model",
			model: models.Model{
				ModelID:    "gpt-4",
				Name:       "GPT-4",
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       models.ModelTypeChat,
			},
			wantErr: false,
		},
		{
			name: "valid chat model with compatibilities",
			model: models.Model{
				ModelID:    "gpt-4o",
				Name:       "GPT-4o",
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       models.ModelTypeChat,
				Config: models.ModelConfig{
					Compatibilities: []string{"vision", "tool-call", "reasoning"},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid chat model with unsupported reasoning effort",
			model: models.Model{
				ModelID:    "gpt-5.6-sol",
				Name:       "GPT-5.6-Sol",
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       models.ModelTypeChat,
				Config: models.ModelConfig{
					ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max", "ultra"},
				},
			},
			wantErr: true,
		},
		{
			name: "valid embedding model",
			model: models.Model{
				ModelID:    "text-embedding-ada-002",
				Name:       "Ada Embeddings",
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       models.ModelTypeEmbedding,
				Config:     models.ModelConfig{Dimensions: intPtr(1536)},
			},
			wantErr: false,
		},
		{
			name: "missing model_id",
			model: models.Model{
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       models.ModelTypeChat,
			},
			wantErr: true,
		},
		{
			name: "missing provider_id",
			model: models.Model{
				ModelID: "gpt-4",
				Type:    models.ModelTypeChat,
			},
			wantErr: true,
		},
		{
			name: "invalid provider_id",
			model: models.Model{
				ModelID:    "gpt-4",
				ProviderID: "not-a-uuid",
				Type:       models.ModelTypeChat,
			},
			wantErr: true,
		},
		{
			name: "invalid model type",
			model: models.Model{
				ModelID:    "gpt-4",
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       "invalid",
			},
			wantErr: true,
		},
		{
			name: "embedding model missing dimensions",
			model: models.Model{
				ModelID:    "text-embedding-ada-002",
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       models.ModelTypeEmbedding,
			},
			wantErr: true,
		},
		{
			name: "invalid compatibility",
			model: models.Model{
				ModelID:    "gpt-4",
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       models.ModelTypeChat,
				Config: models.ModelConfig{
					Compatibilities: []string{"vision", "smell"},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.model.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestModel_HasCompatibility(t *testing.T) {
	m := models.Model{
		Config: models.ModelConfig{
			Compatibilities: []string{"vision", "tool-call", "reasoning"},
		},
	}
	assert.True(t, m.HasCompatibility("vision"))
	assert.True(t, m.HasCompatibility("tool-call"))
	assert.True(t, m.HasCompatibility("reasoning"))
	assert.False(t, m.HasCompatibility("image-output"))
}

func TestModelConfigContextBudgetMaxTokens(t *testing.T) {
	t.Parallel()

	zero := 0
	positive := 128000
	for _, tt := range []struct {
		name   string
		config models.ModelConfig
		want   int
	}{
		{name: "missing", config: models.ModelConfig{}, want: 0},
		{name: "zero", config: models.ModelConfig{ContextWindow: &zero}, want: 0},
		{name: "positive", config: models.ModelConfig{ContextWindow: &positive}, want: positive},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.config.ContextBudgetMaxTokens(); got != tt.want {
				t.Fatalf("ContextBudgetMaxTokens() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestModelTypes(t *testing.T) {
	t.Run("ModelType constants", func(t *testing.T) {
		assert.Equal(t, models.ModelTypeChat, models.ModelType("chat"))
		assert.Equal(t, models.ModelTypeEmbedding, models.ModelType("embedding"))
	})

	t.Run("ClientType constants", func(t *testing.T) {
		assert.Equal(t, models.ClientTypeOpenAIResponses, models.ClientType("openai-responses"))
		assert.Equal(t, models.ClientTypeOpenAICompletions, models.ClientType("openai-completions"))
		assert.Equal(t, models.ClientTypeAnthropicMessages, models.ClientType("anthropic-messages"))
		assert.Equal(t, models.ClientTypeGoogleGenerativeAI, models.ClientType("google-generative-ai"))
	})
}

func TestEnforcesMaxOutputTokens(t *testing.T) {
	t.Parallel()

	assert.False(t, models.EnforcesMaxOutputTokens(models.ClientTypeOpenAICodex))
	assert.True(t, models.EnforcesMaxOutputTokens(models.ClientTypeOpenAIResponses))
}
