package model_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	modeldomain "github.com/memohai/memoh/domains/model"
)

func intPtr(v int) *int { return &v }

func TestModelTypeConstants(t *testing.T) {
	t.Parallel()
	assert.Equal(t, modeldomain.ModelTypeChat, modeldomain.ModelType("chat"))
	assert.Equal(t, modeldomain.ModelTypeEmbedding, modeldomain.ModelType("embedding"))
	assert.Equal(t, modeldomain.ModelTypeSpeech, modeldomain.ModelType("speech"))
	assert.Equal(t, modeldomain.ModelTypeTranscription, modeldomain.ModelType("transcription"))
	assert.Equal(t, modeldomain.ModelTypeVideo, modeldomain.ModelType("video"))
}

func TestClientTypeConstants(t *testing.T) {
	t.Parallel()
	assert.Equal(t, modeldomain.ClientTypeOpenAIResponses, modeldomain.ClientType("openai-responses"))
	assert.Equal(t, modeldomain.ClientTypeOpenAICompletions, modeldomain.ClientType("openai-completions"))
	assert.Equal(t, modeldomain.ClientTypeAnthropicMessages, modeldomain.ClientType("anthropic-messages"))
	assert.Equal(t, modeldomain.ClientTypeGoogleGenerativeAI, modeldomain.ClientType("google-generative-ai"))
	assert.Equal(t, modeldomain.ClientTypeOpenAICodex, modeldomain.ClientType("openai-codex"))
	assert.Equal(t, modeldomain.ClientTypeGitHubCopilot, modeldomain.ClientType("github-copilot"))
	assert.Equal(t, modeldomain.ClientTypeEdgeSpeech, modeldomain.ClientType("edge-speech"))
	assert.Equal(t, modeldomain.ClientTypeOpenAISpeech, modeldomain.ClientType("openai-speech"))
	assert.Equal(t, modeldomain.ClientTypeOpenAITranscription, modeldomain.ClientType("openai-transcription"))
	assert.Equal(t, modeldomain.ClientTypeOpenRouterSpeech, modeldomain.ClientType("openrouter-speech"))
	assert.Equal(t, modeldomain.ClientTypeOpenRouterTranscription, modeldomain.ClientType("openrouter-transcription"))
	assert.Equal(t, modeldomain.ClientTypeElevenLabsSpeech, modeldomain.ClientType("elevenlabs-speech"))
	assert.Equal(t, modeldomain.ClientTypeElevenLabsTranscription, modeldomain.ClientType("elevenlabs-transcription"))
	assert.Equal(t, modeldomain.ClientTypeDeepgramSpeech, modeldomain.ClientType("deepgram-speech"))
	assert.Equal(t, modeldomain.ClientTypeDeepgramTranscription, modeldomain.ClientType("deepgram-transcription"))
	assert.Equal(t, modeldomain.ClientTypeMiniMaxSpeech, modeldomain.ClientType("minimax-speech"))
	assert.Equal(t, modeldomain.ClientTypeVolcengineSpeech, modeldomain.ClientType("volcengine-speech"))
	assert.Equal(t, modeldomain.ClientTypeAlibabaSpeech, modeldomain.ClientType("alibabacloud-speech"))
	assert.Equal(t, modeldomain.ClientTypeMicrosoftSpeech, modeldomain.ClientType("microsoft-speech"))
	assert.Equal(t, modeldomain.ClientTypeGoogleSpeech, modeldomain.ClientType("google-speech"))
	assert.Equal(t, modeldomain.ClientTypeGoogleTranscription, modeldomain.ClientType("google-transcription"))
	assert.Equal(t, modeldomain.ClientTypeOpenRouterVideo, modeldomain.ClientType("openrouter-video"))
	assert.Equal(t, modeldomain.ClientTypeModelArkVideo, modeldomain.ClientType("modelark-video"))
	assert.Equal(t, modeldomain.ClientTypeVolcengineVideo, modeldomain.ClientType("volcengine-video"))
}

func TestCompatAndReasoningThinkingConstants(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "vision", modeldomain.CompatVision)
	assert.Equal(t, "tool-call", modeldomain.CompatToolCall)
	assert.Equal(t, "image-output", modeldomain.CompatImageOutput)
	assert.Equal(t, "reasoning", modeldomain.CompatReasoning)

	assert.Equal(t, "none", modeldomain.ReasoningEffortNone)
	assert.Equal(t, "minimal", modeldomain.ReasoningEffortMinimal)
	assert.Equal(t, "low", modeldomain.ReasoningEffortLow)
	assert.Equal(t, "medium", modeldomain.ReasoningEffortMedium)
	assert.Equal(t, "high", modeldomain.ReasoningEffortHigh)
	assert.Equal(t, "xhigh", modeldomain.ReasoningEffortXHigh)
	assert.Equal(t, "max", modeldomain.ReasoningEffortMax)

	assert.Equal(t, "adaptive", modeldomain.ThinkingModeAdaptive)
	assert.Equal(t, "toggle", modeldomain.ThinkingModeToggle)
	assert.Equal(t, "only_adaptive", modeldomain.ThinkingModeOnlyAdaptive)
	assert.Equal(t, "none", modeldomain.ThinkingModeNone)
}

func TestIsValidModelType(t *testing.T) {
	t.Parallel()
	for _, typ := range []modeldomain.ModelType{
		modeldomain.ModelTypeChat,
		modeldomain.ModelTypeEmbedding,
		modeldomain.ModelTypeSpeech,
		modeldomain.ModelTypeTranscription,
		modeldomain.ModelTypeVideo,
	} {
		assert.True(t, modeldomain.IsValidModelType(typ), typ)
	}
	assert.False(t, modeldomain.IsValidModelType("invalid"))
}

func TestIsValidClientType(t *testing.T) {
	t.Parallel()
	assert.True(t, modeldomain.IsValidClientType(modeldomain.ClientTypeOpenAICompletions))
	assert.True(t, modeldomain.IsValidClientType(modeldomain.ClientTypeVolcengineVideo))
	assert.False(t, modeldomain.IsValidClientType("not-a-client"))
}

func TestIsLLMClientType(t *testing.T) {
	t.Parallel()
	assert.True(t, modeldomain.IsLLMClientType(modeldomain.ClientTypeOpenAICompletions))
	assert.True(t, modeldomain.IsLLMClientType(modeldomain.ClientTypeAnthropicMessages))
	assert.True(t, modeldomain.IsLLMClientType(modeldomain.ClientTypeGoogleGenerativeAI))
	assert.False(t, modeldomain.IsLLMClientType(modeldomain.ClientTypeOpenAISpeech))
	assert.False(t, modeldomain.IsLLMClientType(modeldomain.ClientTypeOpenAITranscription))
	assert.False(t, modeldomain.IsLLMClientType(modeldomain.ClientTypeOpenRouterVideo))
	assert.False(t, modeldomain.IsLLMClientType("bogus"))
}

func TestIsValidReasoningEffort(t *testing.T) {
	t.Parallel()
	for _, effort := range []string{
		modeldomain.ReasoningEffortNone,
		modeldomain.ReasoningEffortMinimal,
		modeldomain.ReasoningEffortLow,
		modeldomain.ReasoningEffortMedium,
		modeldomain.ReasoningEffortHigh,
		modeldomain.ReasoningEffortXHigh,
		modeldomain.ReasoningEffortMax,
	} {
		assert.True(t, modeldomain.IsValidReasoningEffort(effort), effort)
	}
	assert.False(t, modeldomain.IsValidReasoningEffort("ultra"))
	assert.False(t, modeldomain.IsValidReasoningEffort(""))
}

func TestModel_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		model   modeldomain.Model
		wantErr bool
	}{
		{
			name: "valid chat model",
			model: modeldomain.Model{
				ModelID:    "gpt-4",
				Name:       "GPT-4",
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       modeldomain.ModelTypeChat,
			},
		},
		{
			name: "valid chat model with compatibilities",
			model: modeldomain.Model{
				ModelID:    "gpt-4o",
				Name:       "GPT-4o",
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       modeldomain.ModelTypeChat,
				Config: modeldomain.ModelConfig{
					Compatibilities: []string{"vision", "tool-call", "reasoning"},
				},
			},
		},
		{
			name: "invalid chat model with unsupported reasoning effort",
			model: modeldomain.Model{
				ModelID:    "gpt-5.6-sol",
				Name:       "GPT-5.6-Sol",
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       modeldomain.ModelTypeChat,
				Config: modeldomain.ModelConfig{
					ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max", "ultra"},
				},
			},
			wantErr: true,
		},
		{
			name: "valid embedding model",
			model: modeldomain.Model{
				ModelID:    "text-embedding-ada-002",
				Name:       "Ada Embeddings",
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       modeldomain.ModelTypeEmbedding,
				Config:     modeldomain.ModelConfig{Dimensions: intPtr(1536)},
			},
		},
		{
			name: "missing model_id",
			model: modeldomain.Model{
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       modeldomain.ModelTypeChat,
			},
			wantErr: true,
		},
		{
			name: "missing provider_id",
			model: modeldomain.Model{
				ModelID: "gpt-4",
				Type:    modeldomain.ModelTypeChat,
			},
			wantErr: true,
		},
		{
			name: "invalid provider_id",
			model: modeldomain.Model{
				ModelID:    "gpt-4",
				ProviderID: "not-a-uuid",
				Type:       modeldomain.ModelTypeChat,
			},
			wantErr: true,
		},
		{
			name: "invalid model type",
			model: modeldomain.Model{
				ModelID:    "gpt-4",
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       "invalid",
			},
			wantErr: true,
		},
		{
			name: "embedding model missing dimensions",
			model: modeldomain.Model{
				ModelID:    "text-embedding-ada-002",
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       modeldomain.ModelTypeEmbedding,
			},
			wantErr: true,
		},
		{
			name: "invalid compatibility",
			model: modeldomain.Model{
				ModelID:    "gpt-4",
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       modeldomain.ModelTypeChat,
				Config: modeldomain.ModelConfig{
					Compatibilities: []string{"vision", "smell"},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid thinking mode",
			model: modeldomain.Model{
				ModelID:    "gpt-4",
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       modeldomain.ModelTypeChat,
				Config:     modeldomain.ModelConfig{ThinkingMode: "weird"},
			},
			wantErr: true,
		},
		{
			name: "valid thinking mode adaptive",
			model: modeldomain.Model{
				ModelID:    "claude",
				ProviderID: "11111111-1111-1111-1111-111111111111",
				Type:       modeldomain.ModelTypeChat,
				Config:     modeldomain.ModelConfig{ThinkingMode: modeldomain.ThinkingModeAdaptive},
			},
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

func TestModel_ValidateAcceptsBothEnableValues(t *testing.T) {
	t.Parallel()
	base := modeldomain.Model{
		ModelID:    "gpt-4",
		Name:       "GPT-4",
		ProviderID: "11111111-1111-1111-1111-111111111111",
		Type:       modeldomain.ModelTypeChat,
	}
	base.Enable = true
	assert.NoError(t, base.Validate())
	base.Enable = false
	assert.NoError(t, base.Validate())
}

func TestModel_HasCompatibility(t *testing.T) {
	t.Parallel()
	m := modeldomain.Model{
		Config: modeldomain.ModelConfig{
			Compatibilities: []string{"vision", "tool-call", "reasoning"},
		},
	}
	assert.True(t, m.HasCompatibility(modeldomain.CompatVision))
	assert.True(t, m.HasCompatibility(modeldomain.CompatToolCall))
	assert.True(t, m.HasCompatibility(modeldomain.CompatReasoning))
	assert.False(t, m.HasCompatibility(modeldomain.CompatImageOutput))
}

func TestModel_SupportsReasoning(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mode   string
		compat []string
		want   bool
	}{
		{name: "toggle", mode: modeldomain.ThinkingModeToggle, want: true},
		{name: "adaptive", mode: modeldomain.ThinkingModeAdaptive, want: true},
		{name: "only_adaptive", mode: modeldomain.ThinkingModeOnlyAdaptive, want: true},
		{name: "none", mode: modeldomain.ThinkingModeNone, want: false},
		{name: "legacy reasoning compat", compat: []string{modeldomain.CompatReasoning}, want: true},
		{name: "legacy without compat", want: false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			m := modeldomain.Model{Config: modeldomain.ModelConfig{
				ThinkingMode:    tt.mode,
				Compatibilities: tt.compat,
			}}
			assert.Equal(t, tt.want, m.SupportsReasoning())
		})
	}
}

func TestModel_ResolveThinkingMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mode   string
		compat []string
		want   string
	}{
		{name: "toggle", mode: modeldomain.ThinkingModeToggle, want: modeldomain.ThinkingModeToggle},
		{name: "adaptive", mode: modeldomain.ThinkingModeAdaptive, want: modeldomain.ThinkingModeAdaptive},
		{name: "none", mode: modeldomain.ThinkingModeNone, want: modeldomain.ThinkingModeNone},
		{name: "only_adaptive maps to adaptive", mode: modeldomain.ThinkingModeOnlyAdaptive, want: modeldomain.ThinkingModeAdaptive},
		{name: "legacy with reasoning", compat: []string{modeldomain.CompatReasoning}, want: modeldomain.ThinkingModeToggle},
		{name: "legacy without reasoning", want: modeldomain.ThinkingModeNone},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			m := modeldomain.Model{Config: modeldomain.ModelConfig{
				ThinkingMode:    tt.mode,
				Compatibilities: tt.compat,
			}}
			assert.Equal(t, tt.want, m.ResolveThinkingMode())
		})
	}
}

func TestModel_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	m := modeldomain.Model{
		ModelID:    "gpt-4",
		Name:       "GPT-4",
		ProviderID: "11111111-1111-1111-1111-111111111111",
		Type:       modeldomain.ModelTypeChat,
		Enable:     false,
		Config: modeldomain.ModelConfig{
			Compatibilities:  []string{modeldomain.CompatVision},
			ReasoningEfforts: []string{modeldomain.ReasoningEffortLow, modeldomain.ReasoningEffortHigh},
			ThinkingMode:     modeldomain.ThinkingModeToggle,
		},
	}
	data, err := json.Marshal(m)
	require.NoError(t, err)

	var decoded modeldomain.Model
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.False(t, decoded.Enable)
	assert.Equal(t, m.Type, decoded.Type)
	assert.Equal(t, m.Config.ThinkingMode, decoded.Config.ThinkingMode)
	assert.Equal(t, m.Config.Compatibilities, decoded.Config.Compatibilities)
	assert.Equal(t, m.Config.ReasoningEfforts, decoded.Config.ReasoningEfforts)

	m.Enable = true
	data, err = json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.True(t, decoded.Enable)
}

func TestModelConfig_JSONTags(t *testing.T) {
	t.Parallel()
	desc := "hello"
	dims := 1536
	available := true
	cfg := modeldomain.ModelConfig{
		Description:      &desc,
		Dimensions:       &dims,
		Compatibilities:  []string{modeldomain.CompatToolCall},
		ContextWindow:    intPtr(128000),
		ReasoningEfforts: []string{modeldomain.ReasoningEffortMedium},
		ThinkingMode:     modeldomain.ThinkingModeAdaptive,
		CatalogAvailable: &available,
	}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	for _, key := range []string{
		"description", "dimensions", "compatibilities", "context_window",
		"reasoning_efforts", "thinking_mode", "catalog_available",
	} {
		_, ok := raw[key]
		assert.True(t, ok, "missing json key %s in %s", key, string(data))
	}
}
