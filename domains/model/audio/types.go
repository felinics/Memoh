package audio

import "time"

// ProviderMetaResponse exposes adapter metadata (from the registry, not DB).
type ProviderMetaResponse struct {
	Provider                  string       `json:"provider"`
	DisplayName               string       `json:"display_name"`
	Description               string       `json:"description"`
	ConfigSchema              ConfigSchema `json:"config_schema,omitempty"`
	DefaultModel              string       `json:"default_model,omitempty"`
	Models                    []ModelInfo  `json:"models,omitempty"`
	DefaultSynthesisModel     string       `json:"default_synthesis_model,omitempty"`
	SynthesisModels           []ModelInfo  `json:"synthesis_models,omitempty"`
	SupportsSynthesisList     bool         `json:"supports_synthesis_list,omitempty"`
	DefaultTranscriptionModel string       `json:"default_transcription_model,omitempty"`
	TranscriptionModels       []ModelInfo  `json:"transcription_models,omitempty"`
	SupportsTranscriptionList bool         `json:"supports_transcription_list,omitempty"`
}

// SpeechProviderResponse represents a speech-capable provider from the unified providers table.
type SpeechProviderResponse struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	ClientType string         `json:"client_type"`
	Icon       string         `json:"icon,omitempty"`
	Enable     bool           `json:"enable"`
	Config     map[string]any `json:"config,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// SpeechModelResponse represents a speech model from the unified models table.
type SpeechModelResponse struct {
	ID           string         `json:"id"`
	ModelID      string         `json:"model_id"`
	Name         string         `json:"name"`
	ProviderID   string         `json:"provider_id"`
	ProviderType string         `json:"provider_type,omitempty"`
	Config       map[string]any `json:"config,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// TranscriptionModelResponse represents a transcription model from the unified models table.
type TranscriptionModelResponse struct {
	ID           string         `json:"id"`
	ModelID      string         `json:"model_id"`
	Name         string         `json:"name"`
	ProviderID   string         `json:"provider_id"`
	ProviderType string         `json:"provider_type,omitempty"`
	Config       map[string]any `json:"config,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// UpdateSpeechProviderRequest is used for updating a speech provider.
type UpdateSpeechProviderRequest struct {
	Name   *string `json:"name,omitempty"`
	Enable *bool   `json:"enable,omitempty"`
}

// UpdateSpeechModelRequest is used for updating a speech model.
type UpdateSpeechModelRequest struct {
	Name   *string        `json:"name,omitempty"`
	Config map[string]any `json:"config,omitempty"`
}

// TestSynthesizeRequest represents a text-to-speech test request.
type TestSynthesizeRequest struct {
	Text   string         `json:"text"`
	Config map[string]any `json:"config,omitempty"`
}

// TestTranscriptionRequest represents an audio-to-text test request.
type TestTranscriptionRequest struct {
	Config map[string]any `json:"config,omitempty"`
}

// TestTranscriptionResponse represents the result of a transcription test.
type TestTranscriptionResponse struct {
	Text            string              `json:"text"`
	Language        string              `json:"language,omitempty"`
	DurationSeconds float64             `json:"duration_seconds,omitempty"`
	Words           []TranscriptionWord `json:"words,omitempty"`
	Metadata        map[string]any      `json:"metadata,omitempty"`
}

// TranscriptionWord represents a single word alignment from a transcription result.
type TranscriptionWord struct {
	Text      string  `json:"text"`
	Start     float64 `json:"start,omitempty"`
	End       float64 `json:"end,omitempty"`
	SpeakerID string  `json:"speaker_id,omitempty"`
}

// ImportModelsResponse represents the response for importing speech models.
type ImportModelsResponse struct {
	Created int      `json:"created"`
	Skipped int      `json:"skipped"`
	Models  []string `json:"models"`
}
