package audio

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	alibabaspeech "github.com/felinics/twilight/provider/alibabacloud/speech"
	deepgramspeech "github.com/felinics/twilight/provider/deepgram/speech"
	deepgramtranscription "github.com/felinics/twilight/provider/deepgram/transcription"
	edgespeech "github.com/felinics/twilight/provider/edge/speech"
	elevenlabsspeech "github.com/felinics/twilight/provider/elevenlabs/speech"
	elevenlabstranscription "github.com/felinics/twilight/provider/elevenlabs/transcription"
	googletranscription "github.com/felinics/twilight/provider/google/transcription"
	microsoftspeech "github.com/felinics/twilight/provider/microsoft/speech"
	minimaxspeech "github.com/felinics/twilight/provider/minimax/speech"
	openaispeech "github.com/felinics/twilight/provider/openai/speech"
	openaitranscription "github.com/felinics/twilight/provider/openai/transcription"
	openrouterspeech "github.com/felinics/twilight/provider/openrouter/speech"
	openroutertranscription "github.com/felinics/twilight/provider/openrouter/transcription"
	volcenginespeech "github.com/felinics/twilight/provider/volcengine/speech"
	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/models"
)

type (
	ProviderFactory              func(config map[string]any) (sdk.SpeechProvider, error)
	TranscriptionProviderFactory func(config map[string]any) (sdk.TranscriptionProvider, error)
)

type ProviderDefinition struct {
	ClientType                models.ClientType
	DisplayName               string
	Icon                      string
	Description               string
	ConfigSchema              ConfigSchema
	DefaultModel              string
	SupportsList              bool
	Models                    []ModelInfo
	Factory                   ProviderFactory
	DefaultTranscriptionModel string
	SupportsTranscriptionList bool
	TranscriptionModels       []ModelInfo
	TranscriptionFactory      TranscriptionProviderFactory
	Order                     int
}

type Registry struct {
	mu        sync.RWMutex
	providers map[models.ClientType]ProviderDefinition
	ordered   []models.ClientType
}

func isTranscriptionClientType(clientType models.ClientType) bool {
	switch clientType {
	case
		models.ClientTypeOpenAITranscription,
		models.ClientTypeOpenRouterTranscription,
		models.ClientTypeElevenLabsTranscription,
		models.ClientTypeDeepgramTranscription,
		models.ClientTypeGoogleTranscription:
		return true
	default:
		return false
	}
}

func speechToTranscriptionClientType(clientType models.ClientType) models.ClientType {
	switch clientType {
	case models.ClientTypeOpenAISpeech:
		return models.ClientTypeOpenAITranscription
	case models.ClientTypeOpenRouterSpeech:
		return models.ClientTypeOpenRouterTranscription
	case models.ClientTypeElevenLabsSpeech:
		return models.ClientTypeElevenLabsTranscription
	case models.ClientTypeDeepgramSpeech:
		return models.ClientTypeDeepgramTranscription
	case models.ClientTypeGoogleSpeech:
		return models.ClientTypeGoogleTranscription
	default:
		return ""
	}
}

func transcriptionDisplayName(displayName string) string {
	displayName = strings.TrimSpace(displayName)
	if displayName == "Google Speech" {
		return "Google Transcription"
	}
	if strings.HasSuffix(displayName, " Speech") {
		return strings.TrimSuffix(displayName, " Speech") + " Transcription"
	}
	return displayName + " Transcription"
}

func NewRegistry() *Registry {
	r := &Registry{
		providers: make(map[models.ClientType]ProviderDefinition),
	}
	baseDefs := defaultProviderDefinitions()
	for _, def := range baseDefs {
		if def.Factory == nil && def.TranscriptionFactory != nil {
			continue
		}
		r.Register(def)
	}
	for _, def := range transcriptionProviderDefinitions(baseDefs) {
		r.Register(def)
	}
	return r
}

func (r *Registry) Register(def ProviderDefinition) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.providers[def.ClientType]; !exists {
		r.ordered = append(r.ordered, def.ClientType)
	}
	r.providers[def.ClientType] = def
	sort.SliceStable(r.ordered, func(i, j int) bool {
		left := r.providers[r.ordered[i]]
		right := r.providers[r.ordered[j]]
		if left.Order != right.Order {
			return left.Order < right.Order
		}
		return left.DisplayName < right.DisplayName
	})
}

func (r *Registry) Get(clientType models.ClientType) (ProviderDefinition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.providers[clientType]
	if !ok {
		return ProviderDefinition{}, fmt.Errorf("speech provider not found: %s", clientType)
	}
	return def, nil
}

func (r *Registry) List() []ProviderDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ProviderDefinition, 0, len(r.ordered))
	for _, key := range r.ordered {
		out = append(out, r.providers[key])
	}
	return out
}

func (r *Registry) ListMeta() []ProviderMetaResponse {
	defs := r.List()
	metas := make([]ProviderMetaResponse, 0, len(defs))
	for _, def := range defs {
		metas = append(metas, ProviderMetaResponse{
			Provider:                  string(def.ClientType),
			DisplayName:               def.DisplayName,
			Description:               def.Description,
			ConfigSchema:              def.ConfigSchema,
			DefaultModel:              def.DefaultModel,
			Models:                    def.Models,
			DefaultSynthesisModel:     def.DefaultModel,
			SynthesisModels:           def.Models,
			SupportsSynthesisList:     def.SupportsList,
			DefaultTranscriptionModel: def.DefaultTranscriptionModel,
			TranscriptionModels:       def.TranscriptionModels,
			SupportsTranscriptionList: def.SupportsTranscriptionList,
		})
	}
	return metas
}

func (r *Registry) ListSpeechMeta() []ProviderMetaResponse {
	defs := r.List()
	metas := make([]ProviderMetaResponse, 0, len(defs))
	for _, def := range defs {
		if def.Factory == nil {
			continue
		}
		metas = append(metas, ProviderMetaResponse{
			Provider:              string(def.ClientType),
			DisplayName:           def.DisplayName,
			Description:           def.Description,
			ConfigSchema:          def.ConfigSchema,
			DefaultModel:          def.DefaultModel,
			Models:                def.Models,
			DefaultSynthesisModel: def.DefaultModel,
			SynthesisModels:       def.Models,
			SupportsSynthesisList: def.SupportsList,
		})
	}
	return metas
}

func (r *Registry) ListTranscriptionMeta() []ProviderMetaResponse {
	defs := r.List()
	metas := make([]ProviderMetaResponse, 0, len(defs))
	for _, def := range defs {
		if def.TranscriptionFactory == nil || !isTranscriptionClientType(def.ClientType) {
			continue
		}
		modelsList := def.TranscriptionModels
		if len(modelsList) == 0 {
			modelsList = def.Models
		}
		metas = append(metas, ProviderMetaResponse{
			Provider:                  string(def.ClientType),
			DisplayName:               def.DisplayName,
			Description:               def.Description,
			ConfigSchema:              def.ConfigSchema,
			DefaultModel:              def.DefaultTranscriptionModel,
			Models:                    modelsList,
			DefaultTranscriptionModel: def.DefaultTranscriptionModel,
			TranscriptionModels:       modelsList,
			SupportsTranscriptionList: def.SupportsTranscriptionList,
		})
	}
	return metas
}

func transcriptionProviderDefinitions(base []ProviderDefinition) []ProviderDefinition {
	out := make([]ProviderDefinition, 0, len(base))
	for _, def := range base {
		clientType := speechToTranscriptionClientType(def.ClientType)
		if clientType == "" || def.TranscriptionFactory == nil {
			continue
		}
		modelsList := def.TranscriptionModels
		out = append(out, ProviderDefinition{
			ClientType:                clientType,
			DisplayName:               transcriptionDisplayName(def.DisplayName),
			Icon:                      def.Icon,
			Description:               strings.TrimSpace(def.Description),
			ConfigSchema:              def.ConfigSchema,
			DefaultModel:              def.DefaultTranscriptionModel,
			SupportsList:              def.SupportsTranscriptionList,
			Models:                    modelsList,
			DefaultTranscriptionModel: def.DefaultTranscriptionModel,
			SupportsTranscriptionList: def.SupportsTranscriptionList,
			TranscriptionModels:       modelsList,
			TranscriptionFactory:      def.TranscriptionFactory,
			Order:                     def.Order + 1,
		})
	}
	return out
}

func defaultProviderDefinitions() []ProviderDefinition {
	edgeVoices := make([]VoiceInfo, 0)
	for lang, ids := range edgespeech.EdgeTTSVoices {
		for _, id := range ids {
			name := strings.TrimPrefix(id, lang+"-")
			name = strings.TrimSuffix(name, "Neural")
			edgeVoices = append(edgeVoices, VoiceInfo{ID: id, Lang: lang, Name: name})
		}
	}
	sort.Slice(edgeVoices, func(i, j int) bool {
		if edgeVoices[i].Lang != edgeVoices[j].Lang {
			return edgeVoices[i].Lang < edgeVoices[j].Lang
		}
		return edgeVoices[i].ID < edgeVoices[j].ID
	})

	return []ProviderDefinition{
		{
			ClientType:   models.ClientTypeEdgeSpeech,
			DisplayName:  "Microsoft Edge",
			Icon:         "microsoft",
			Description:  "Free Edge Read Aloud TTS",
			ConfigSchema: ConfigSchema{Fields: []FieldSchema{stringField("base_url", "Base URL", "Override the Edge WebSocket endpoint", false, "", 10)}},
			DefaultModel: "edge-read-aloud",
			SupportsList: false,
			Models: []ModelInfo{{
				ID:          "edge-read-aloud",
				Name:        "Edge Read Aloud",
				Description: "Built-in Edge Read Aloud speech model",
				ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					enumField("voice", "Voice", "Edge voice ID", false, voiceIDs(edgeVoices), 10),
					stringField("language", "Language", "Optional BCP-47 language tag", false, "en-US", 20),
					enumField("format", "Format", "Output audio format", false, []string{"audio-24khz-48kbitrate-mono-mp3", "audio-24khz-96kbitrate-mono-mp3", "webm-24khz-16bit-mono-opus"}, 30),
					numberField("speed", "Speed", "Speech rate, 1.0 = normal", false, 1.0, 40),
					numberField("pitch", "Pitch", "Pitch adjustment in Hz", false, 0, 50),
				}},
				Capabilities: ModelCapabilities{
					ConfigSchema: ConfigSchema{Fields: []FieldSchema{
						enumField("voice", "Voice", "Edge voice ID", false, voiceIDs(edgeVoices), 10),
						stringField("language", "Language", "Optional BCP-47 language tag", false, "en-US", 20),
						enumField("format", "Format", "Output audio format", false, []string{"audio-24khz-48kbitrate-mono-mp3", "audio-24khz-96kbitrate-mono-mp3", "webm-24khz-16bit-mono-opus"}, 30),
						numberField("speed", "Speed", "Speech rate, 1.0 = normal", false, 1.0, 40),
						numberField("pitch", "Pitch", "Pitch adjustment in Hz", false, 0, 50),
					}},
					Voices:  edgeVoices,
					Formats: []string{"audio-24khz-48kbitrate-mono-mp3", "audio-24khz-96kbitrate-mono-mp3", "webm-24khz-16bit-mono-opus"},
					Speed:   &ParamConstraint{Options: []float64{0.5, 1.0, 2.0, 3.0}, Default: 1.0},
					Pitch:   &ParamConstraint{Min: -100, Max: 100, Default: 0},
				},
			}},
			Factory: func(config map[string]any) (sdk.SpeechProvider, error) {
				opts := []edgespeech.Option{}
				if v := configString(config, "base_url"); v != "" {
					opts = append(opts, edgespeech.WithBaseURL(v))
				}
				return edgespeech.New(opts...), nil
			},
			Order: 10,
		},
		{
			ClientType:  models.ClientTypeOpenAISpeech,
			DisplayName: "OpenAI Speech",
			Icon:        "openai",
			Description: "OpenAI /audio/speech compatible TTS",
			ConfigSchema: ConfigSchema{Fields: []FieldSchema{
				secretField("api_key", "API Key", "Bearer API key", true, 10),
				stringField("base_url", "Base URL", "Override the API base URL", false, "https://api.openai.com/v1", 20),
			}},
			DefaultModel:              "gpt-4o-mini-tts",
			SupportsList:              true,
			DefaultTranscriptionModel: "gpt-4o-mini-transcribe",
			SupportsTranscriptionList: true,
			Models: []ModelInfo{{
				ID:          "gpt-4o-mini-tts",
				Name:        "gpt-4o-mini-tts",
				Description: "Default OpenAI speech model",
				ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					stringField("voice", "Voice", "Voice ID", false, "coral", 10),
					enumField("response_format", "Response Format", "Audio format", false, []string{"mp3", "opus", "pcm", "wav"}, 20),
					numberField("speed", "Speed", "Speech rate", false, 1.0, 30),
					stringField("instructions", "Instructions", "Style instructions for supported models", false, "", 40),
				}},
				Capabilities: ModelCapabilities{
					ConfigSchema: ConfigSchema{Fields: []FieldSchema{
						stringField("voice", "Voice", "Voice ID", false, "coral", 10),
						enumField("response_format", "Response Format", "Audio format", false, []string{"mp3", "opus", "pcm", "wav"}, 20),
						numberField("speed", "Speed", "Speech rate", false, 1.0, 30),
						stringField("instructions", "Instructions", "Style instructions for supported models", false, "", 40),
					}},
					Formats: []string{"mp3", "opus", "pcm", "wav"},
				},
			}},
			TranscriptionModels: []ModelInfo{{
				ID:          "gpt-4o-mini-transcribe",
				Name:        "gpt-4o-mini-transcribe",
				Description: "Default OpenAI transcription model",
				ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					stringField("language", "Language", "Optional ISO language hint", false, "", 10),
					stringField("prompt", "Prompt", "Optional prompt to guide transcription", false, "", 20),
					numberField("temperature", "Temperature", "Sampling temperature", false, 0, 30),
					enumField("response_format", "Response Format", "Transcription response format", false, []string{"json", "verbose_json", "text", "srt", "vtt"}, 40),
				}},
				Capabilities: ModelCapabilities{ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					stringField("language", "Language", "Optional ISO language hint", false, "", 10),
					stringField("prompt", "Prompt", "Optional prompt to guide transcription", false, "", 20),
					numberField("temperature", "Temperature", "Sampling temperature", false, 0, 30),
					enumField("response_format", "Response Format", "Transcription response format", false, []string{"json", "verbose_json", "text", "srt", "vtt"}, 40),
				}}},
			}},
			Factory: func(config map[string]any) (sdk.SpeechProvider, error) {
				opts := []openaispeech.Option{}
				if v := configString(config, "api_key"); v != "" {
					opts = append(opts, openaispeech.WithAPIKey(v))
				}
				if v := configString(config, "base_url"); v != "" {
					opts = append(opts, openaispeech.WithBaseURL(v))
				}
				return openaispeech.New(opts...), nil
			},
			TranscriptionFactory: func(config map[string]any) (sdk.TranscriptionProvider, error) {
				opts := []openaitranscription.Option{}
				if v := configString(config, "api_key"); v != "" {
					opts = append(opts, openaitranscription.WithAPIKey(v))
				}
				if v := configString(config, "base_url"); v != "" {
					opts = append(opts, openaitranscription.WithBaseURL(v))
				}
				return openaitranscription.New(opts...), nil
			},
			Order: 20,
		},
		{
			ClientType:  models.ClientTypeOpenRouterSpeech,
			DisplayName: "OpenRouter Speech",
			Icon:        "openrouter",
			Description: "OpenRouter audio modality TTS",
			ConfigSchema: ConfigSchema{Fields: []FieldSchema{
				secretField("api_key", "API Key", "OpenRouter API key", true, 10),
				stringField("base_url", "Base URL", "Override the API base URL", false, "https://openrouter.ai/api/v1", 20),
			}},
			DefaultModel:              "openrouter-tts",
			SupportsList:              true,
			DefaultTranscriptionModel: "openai/gpt-4o-mini-transcribe",
			SupportsTranscriptionList: true,
			Models: []ModelInfo{{
				ID:           "openrouter-tts",
				Name:         "openrouter-tts",
				Description:  "Default OpenRouter speech wrapper model",
				TemplateOnly: true,
				ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					advancedStringField("model", "Model", "Underlying OpenRouter model ID", false, "openai/gpt-audio-mini", 10),
					stringField("voice", "Voice", "Voice name", false, "coral", 20),
					numberField("speed", "Speed", "Speech rate", false, 1.0, 30),
				}},
				Capabilities: ModelCapabilities{ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					advancedStringField("model", "Model", "Underlying OpenRouter model ID", false, "openai/gpt-audio-mini", 10),
					stringField("voice", "Voice", "Voice name", false, "coral", 20),
					numberField("speed", "Speed", "Speech rate", false, 1.0, 30),
				}}},
			}},
			TranscriptionModels: []ModelInfo{{
				ID:          "openai/gpt-4o-mini-transcribe",
				Name:        "openai/gpt-4o-mini-transcribe",
				Description: "Default OpenRouter transcription model",
				ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					advancedStringField("prompt", "Prompt", "Prompt passed to the model before audio input", false, "", 10),
				}},
				Capabilities: ModelCapabilities{ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					advancedStringField("prompt", "Prompt", "Prompt passed to the model before audio input", false, "", 10),
				}}},
			}},
			Factory: func(config map[string]any) (sdk.SpeechProvider, error) {
				opts := []openrouterspeech.Option{}
				if v := configString(config, "api_key"); v != "" {
					opts = append(opts, openrouterspeech.WithAPIKey(v))
				}
				if v := configString(config, "base_url"); v != "" {
					opts = append(opts, openrouterspeech.WithBaseURL(v))
				}
				return openrouterspeech.New(opts...), nil
			},
			TranscriptionFactory: func(config map[string]any) (sdk.TranscriptionProvider, error) {
				opts := []openroutertranscription.Option{}
				if v := configString(config, "api_key"); v != "" {
					opts = append(opts, openroutertranscription.WithAPIKey(v))
				}
				if v := configString(config, "base_url"); v != "" {
					opts = append(opts, openroutertranscription.WithBaseURL(v))
				}
				return openroutertranscription.New(opts...), nil
			},
			Order: 30,
		},
		{
			ClientType:  models.ClientTypeElevenLabsSpeech,
			DisplayName: "ElevenLabs Speech",
			Icon:        "elevenlabs",
			Description: "ElevenLabs text-to-speech",
			ConfigSchema: ConfigSchema{Fields: []FieldSchema{
				secretField("api_key", "API Key", "ElevenLabs API key", true, 10),
				stringField("base_url", "Base URL", "Override the API base URL", false, "https://api.elevenlabs.io", 20),
			}},
			DefaultModel:              "elevenlabs-tts",
			SupportsList:              true,
			DefaultTranscriptionModel: "scribe_v2",
			SupportsTranscriptionList: true,
			Models: []ModelInfo{{
				ID:           "elevenlabs-tts",
				Name:         "elevenlabs-tts",
				Description:  "Default ElevenLabs speech wrapper model",
				TemplateOnly: true,
				ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					stringField("voice_id", "Voice ID", "ElevenLabs voice ID", true, "", 10),
					advancedStringField("model_id", "Model ID", "ElevenLabs model ID", false, "eleven_multilingual_v2", 20),
					numberField("stability", "Stability", "Voice stability 0-1", false, 0.5, 30),
					numberField("similarity_boost", "Similarity Boost", "Voice similarity boost 0-1", false, 0.75, 40),
					numberField("style", "Style", "Speaking style intensity 0-1", false, 0, 50),
					boolField("use_speaker_boost", "Speaker Boost", "Enable speaker boost", false, 60),
					numberField("speed", "Speed", "Speech rate 0.5-2.0", false, 1.0, 70),
					stringField("output_format", "Output Format", "Output format", false, "mp3_44100_128", 80),
					numberField("seed", "Seed", "Deterministic seed", false, 0, 90),
					enumField("apply_text_normalization", "Text Normalization", "Text normalization mode", false, []string{"auto", "on", "off"}, 100),
					stringField("language_code", "Language Code", "Optional BCP-47 language code", false, "en-US", 110),
				}},
				Capabilities: ModelCapabilities{ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					stringField("voice_id", "Voice ID", "ElevenLabs voice ID", true, "", 10),
					advancedStringField("model_id", "Model ID", "ElevenLabs model ID", false, "eleven_multilingual_v2", 20),
					numberField("stability", "Stability", "Voice stability 0-1", false, 0.5, 30),
					numberField("similarity_boost", "Similarity Boost", "Voice similarity boost 0-1", false, 0.75, 40),
					numberField("style", "Style", "Speaking style intensity 0-1", false, 0, 50),
					boolField("use_speaker_boost", "Speaker Boost", "Enable speaker boost", false, 60),
					numberField("speed", "Speed", "Speech rate 0.5-2.0", false, 1.0, 70),
					stringField("output_format", "Output Format", "Output format", false, "mp3_44100_128", 80),
					numberField("seed", "Seed", "Deterministic seed", false, 0, 90),
					enumField("apply_text_normalization", "Text Normalization", "Text normalization mode", false, []string{"auto", "on", "off"}, 100),
					stringField("language_code", "Language Code", "Optional BCP-47 language code", false, "en-US", 110),
				}}},
			}},
			TranscriptionModels: []ModelInfo{{
				ID:          "scribe_v2",
				Name:        "scribe_v2",
				Description: "Default ElevenLabs transcription model",
				ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					stringField("language_code", "Language Code", "Optional BCP-47 language code", false, "", 10),
					boolField("tag_audio_events", "Tag Audio Events", "Include non-speech events in timestamps", false, 20),
					boolField("diarize", "Diarize", "Enable speaker diarization", false, 30),
					numberField("num_speakers", "Number of Speakers", "Optional expected speaker count", false, 0, 40),
					enumField("timestamps_granularity", "Timestamps Granularity", "Timestamps granularity", false, []string{"word", "character"}, 50),
				}},
				Capabilities: ModelCapabilities{ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					stringField("language_code", "Language Code", "Optional BCP-47 language code", false, "", 10),
					boolField("tag_audio_events", "Tag Audio Events", "Include non-speech events in timestamps", false, 20),
					boolField("diarize", "Diarize", "Enable speaker diarization", false, 30),
					numberField("num_speakers", "Number of Speakers", "Optional expected speaker count", false, 0, 40),
					enumField("timestamps_granularity", "Timestamps Granularity", "Timestamps granularity", false, []string{"word", "character"}, 50),
				}}},
			}},
			Factory: func(config map[string]any) (sdk.SpeechProvider, error) {
				opts := []elevenlabsspeech.Option{}
				if v := configString(config, "api_key"); v != "" {
					opts = append(opts, elevenlabsspeech.WithAPIKey(v))
				}
				if v := configString(config, "base_url"); v != "" {
					opts = append(opts, elevenlabsspeech.WithBaseURL(v))
				}
				return elevenlabsspeech.New(opts...), nil
			},
			TranscriptionFactory: func(config map[string]any) (sdk.TranscriptionProvider, error) {
				opts := []elevenlabstranscription.Option{}
				if v := configString(config, "api_key"); v != "" {
					opts = append(opts, elevenlabstranscription.WithAPIKey(v))
				}
				if v := configString(config, "base_url"); v != "" {
					opts = append(opts, elevenlabstranscription.WithBaseURL(v))
				}
				return elevenlabstranscription.New(opts...), nil
			},
			Order: 40,
		},
		{
			ClientType:  models.ClientTypeGoogleSpeech,
			DisplayName: "Google Speech",
			Icon:        "google-color",
			Description: "Google Gemini speech transcription",
			ConfigSchema: ConfigSchema{Fields: []FieldSchema{
				secretField("api_key", "API Key", "Google API key", true, 10),
				stringField("base_url", "Base URL", "Override the API base URL", false, "https://generativelanguage.googleapis.com/v1beta", 20),
			}},
			DefaultTranscriptionModel: "gemini-2.5-flash",
			SupportsTranscriptionList: true,
			TranscriptionModels: []ModelInfo{{
				ID:          "gemini-2.5-flash",
				Name:        "gemini-2.5-flash",
				Description: "Default Google transcription model",
				ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					advancedStringField("prompt", "Prompt", "Prompt passed alongside audio", false, "", 10),
				}},
				Capabilities: ModelCapabilities{ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					advancedStringField("prompt", "Prompt", "Prompt passed alongside audio", false, "", 10),
				}}},
			}},
			TranscriptionFactory: func(config map[string]any) (sdk.TranscriptionProvider, error) {
				opts := []googletranscription.Option{}
				if v := configString(config, "api_key"); v != "" {
					opts = append(opts, googletranscription.WithAPIKey(v))
				}
				if v := configString(config, "base_url"); v != "" {
					opts = append(opts, googletranscription.WithBaseURL(v))
				}
				return googletranscription.New(opts...), nil
			},
			Order: 45,
		},
		{
			ClientType:  models.ClientTypeDeepgramSpeech,
			DisplayName: "Deepgram Speech",
			Icon:        "deepgram",
			Description: "Deepgram TTS",
			ConfigSchema: ConfigSchema{Fields: []FieldSchema{
				secretField("api_key", "API Key", "Deepgram API key", true, 10),
				stringField("base_url", "Base URL", "Override the API base URL", false, "https://api.deepgram.com", 20),
			}},
			DefaultModel:              "deepgram-tts",
			SupportsList:              false,
			DefaultTranscriptionModel: "nova-3",
			SupportsTranscriptionList: false,
			Models: []ModelInfo{{
				ID:          "deepgram-tts",
				Name:        "deepgram-tts",
				Description: "Default Deepgram speech wrapper model",
				ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					advancedStringField("model", "Model", "Deepgram voice model", false, "aura-2-asteria-en", 10),
					enumField("encoding", "Encoding", "Audio encoding", false, []string{"linear16", "mulaw", "alaw"}, 20),
					numberField("sample_rate", "Sample Rate", "Audio sample rate in Hz", false, 24000, 30),
					enumField("container", "Container", "Audio container", false, []string{"wav", "none"}, 40),
				}},
				Capabilities: ModelCapabilities{
					ConfigSchema: ConfigSchema{Fields: []FieldSchema{
						advancedStringField("model", "Model", "Deepgram voice model", false, "aura-2-asteria-en", 10),
						enumField("encoding", "Encoding", "Audio encoding", false, []string{"linear16", "mulaw", "alaw"}, 20),
						numberField("sample_rate", "Sample Rate", "Audio sample rate in Hz", false, 24000, 30),
						enumField("container", "Container", "Audio container", false, []string{"wav", "none"}, 40),
					}},
					Formats: []string{"wav", "none"},
				},
			}},
			TranscriptionModels: []ModelInfo{{
				ID:          "nova-3",
				Name:        "nova-3",
				Description: "Default Deepgram transcription model",
				ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					stringField("language", "Language", "Optional language hint", false, "", 10),
					boolField("smart_format", "Smart Format", "Enable smart formatting", false, 20),
					boolField("detect_language", "Detect Language", "Enable automatic language detection", false, 30),
					boolField("diarize", "Diarize", "Enable speaker diarization", false, 40),
					boolField("punctuate", "Punctuate", "Enable punctuation", false, 50),
				}},
				Capabilities: ModelCapabilities{ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					stringField("language", "Language", "Optional language hint", false, "", 10),
					boolField("smart_format", "Smart Format", "Enable smart formatting", false, 20),
					boolField("detect_language", "Detect Language", "Enable automatic language detection", false, 30),
					boolField("diarize", "Diarize", "Enable speaker diarization", false, 40),
					boolField("punctuate", "Punctuate", "Enable punctuation", false, 50),
				}}},
			}},
			Factory: func(config map[string]any) (sdk.SpeechProvider, error) {
				opts := []deepgramspeech.Option{}
				if v := configString(config, "api_key"); v != "" {
					opts = append(opts, deepgramspeech.WithAPIKey(v))
				}
				if v := configString(config, "base_url"); v != "" {
					opts = append(opts, deepgramspeech.WithBaseURL(v))
				}
				return deepgramspeech.New(opts...), nil
			},
			TranscriptionFactory: func(config map[string]any) (sdk.TranscriptionProvider, error) {
				opts := []deepgramtranscription.Option{}
				if v := configString(config, "api_key"); v != "" {
					opts = append(opts, deepgramtranscription.WithAPIKey(v))
				}
				if v := configString(config, "base_url"); v != "" {
					opts = append(opts, deepgramtranscription.WithBaseURL(v))
				}
				return deepgramtranscription.New(opts...), nil
			},
			Order: 50,
		},
		{
			ClientType:  models.ClientTypeMiniMaxSpeech,
			DisplayName: "MiniMax Speech",
			Icon:        "minimax-color",
			Description: "MiniMax TTS",
			ConfigSchema: ConfigSchema{Fields: []FieldSchema{
				secretField("api_key", "API Key", "MiniMax API key", true, 10),
				stringField("base_url", "Base URL", "Override the API base URL", false, "https://api.minimax.io", 20),
			}},
			DefaultModel: "minimax-tts",
			SupportsList: false,
			Models: []ModelInfo{{
				ID:          "minimax-tts",
				Name:        "minimax-tts",
				Description: "Default MiniMax speech wrapper model",
				ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					stringField("voice_id", "Voice ID", "MiniMax voice ID", false, "English_expressive_narrator", 10),
					advancedStringField("model", "Model", "MiniMax model", false, "speech-2.8-hd", 20),
					numberField("speed", "Speed", "Speech rate", false, 1.0, 30),
					numberField("vol", "Volume", "Volume", false, 1.0, 40),
					numberField("pitch", "Pitch", "Pitch adjustment", false, 0, 50),
					enumField("output_format", "Output Format", "Audio format", false, []string{"mp3", "pcm", "flac", "wav"}, 60),
					numberField("sample_rate", "Sample Rate", "Audio sample rate", false, 32000, 70),
				}},
				Capabilities: ModelCapabilities{
					ConfigSchema: ConfigSchema{Fields: []FieldSchema{
						stringField("voice_id", "Voice ID", "MiniMax voice ID", false, "English_expressive_narrator", 10),
						advancedStringField("model", "Model", "MiniMax model", false, "speech-2.8-hd", 20),
						numberField("speed", "Speed", "Speech rate", false, 1.0, 30),
						numberField("vol", "Volume", "Volume", false, 1.0, 40),
						numberField("pitch", "Pitch", "Pitch adjustment", false, 0, 50),
						enumField("output_format", "Output Format", "Audio format", false, []string{"mp3", "pcm", "flac", "wav"}, 60),
						numberField("sample_rate", "Sample Rate", "Audio sample rate", false, 32000, 70),
					}},
					Formats: []string{"mp3", "pcm", "flac", "wav"},
				},
			}},
			Factory: func(config map[string]any) (sdk.SpeechProvider, error) {
				opts := []minimaxspeech.Option{}
				if v := configString(config, "api_key"); v != "" {
					opts = append(opts, minimaxspeech.WithAPIKey(v))
				}
				if v := configString(config, "base_url"); v != "" {
					opts = append(opts, minimaxspeech.WithBaseURL(v))
				}
				return minimaxspeech.New(opts...), nil
			},
			Order: 60,
		},
		{
			ClientType:  models.ClientTypeVolcengineSpeech,
			DisplayName: "Volcengine Speech",
			Icon:        "volcengine-color",
			Description: "Volcengine SAMI TTS",
			ConfigSchema: ConfigSchema{Fields: []FieldSchema{
				secretField("access_key", "Access Key", "Volcengine access key", true, 10),
				secretField("secret_key", "Secret Key", "Volcengine secret key", true, 20),
				secretField("app_key", "App Key", "SAMI app key", true, 30),
				stringField("base_url", "Base URL", "Override the API base URL", false, "https://sami.bytedance.com", 40),
			}},
			DefaultModel: "sami-tts",
			SupportsList: false,
			Models: []ModelInfo{{
				ID:          "sami-tts",
				Name:        "sami-tts",
				Description: "Default Volcengine SAMI speech wrapper model",
				ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					stringField("speaker", "Speaker", "Speaker ID", true, "", 10),
					enumField("encoding", "Encoding", "Output encoding", false, []string{"mp3", "wav", "aac"}, 20),
					numberField("sample_rate", "Sample Rate", "Audio sample rate", false, 24000, 30),
					numberField("speech_rate", "Speech Rate", "Speech rate [-50,100]", false, 0, 40),
					numberField("pitch_rate", "Pitch Rate", "Pitch rate [-12,12]", false, 0, 50),
				}},
				Capabilities: ModelCapabilities{
					ConfigSchema: ConfigSchema{Fields: []FieldSchema{
						stringField("speaker", "Speaker", "Speaker ID", true, "", 10),
						enumField("encoding", "Encoding", "Output encoding", false, []string{"mp3", "wav", "aac"}, 20),
						numberField("sample_rate", "Sample Rate", "Audio sample rate", false, 24000, 30),
						numberField("speech_rate", "Speech Rate", "Speech rate [-50,100]", false, 0, 40),
						numberField("pitch_rate", "Pitch Rate", "Pitch rate [-12,12]", false, 0, 50),
					}},
					Formats: []string{"mp3", "wav", "aac"},
				},
			}},
			Factory: func(config map[string]any) (sdk.SpeechProvider, error) {
				opts := []volcenginespeech.Option{}
				if v := configString(config, "access_key"); v != "" {
					opts = append(opts, volcenginespeech.WithAccessKey(v))
				}
				if v := configString(config, "secret_key"); v != "" {
					opts = append(opts, volcenginespeech.WithSecretKey(v))
				}
				if v := configString(config, "app_key"); v != "" {
					opts = append(opts, volcenginespeech.WithAppKey(v))
				}
				if v := configString(config, "base_url"); v != "" {
					opts = append(opts, volcenginespeech.WithBaseURL(v))
				}
				return volcenginespeech.New(opts...), nil
			},
			Order: 70,
		},
		{
			ClientType:  models.ClientTypeAlibabaSpeech,
			DisplayName: "Alibaba Cloud Speech",
			Icon:        "bailian-color",
			Description: "DashScope CosyVoice TTS",
			ConfigSchema: ConfigSchema{Fields: []FieldSchema{
				secretField("api_key", "API Key", "DashScope API key", true, 10),
				stringField("base_url", "Base URL", "Override the WebSocket endpoint", false, "wss://dashscope.aliyuncs.com/api-ws/v1/inference/", 20),
			}},
			DefaultModel: "cosyvoice-tts",
			SupportsList: false,
			Models: []ModelInfo{{
				ID:          "cosyvoice-tts",
				Name:        "cosyvoice-tts",
				Description: "Default DashScope CosyVoice wrapper model",
				ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					advancedStringField("model", "Model", "DashScope model ID", false, "cosyvoice-v1", 10),
					stringField("voice", "Voice", "Voice or custom clone ID", true, "", 20),
					enumField("format", "Format", "Audio format", false, []string{"mp3", "wav", "pcm", "opus"}, 30),
					numberField("sample_rate", "Sample Rate", "Audio sample rate", false, 22050, 40),
					numberField("volume", "Volume", "Volume 0-100", false, 50, 50),
					numberField("rate", "Rate", "Speech rate 0.5-2.0", false, 1.0, 60),
					numberField("pitch", "Pitch", "Pitch multiplier 0.5-2.0", false, 1.0, 70),
				}},
				Capabilities: ModelCapabilities{
					ConfigSchema: ConfigSchema{Fields: []FieldSchema{
						advancedStringField("model", "Model", "DashScope model ID", false, "cosyvoice-v1", 10),
						stringField("voice", "Voice", "Voice or custom clone ID", true, "", 20),
						enumField("format", "Format", "Audio format", false, []string{"mp3", "wav", "pcm", "opus"}, 30),
						numberField("sample_rate", "Sample Rate", "Audio sample rate", false, 22050, 40),
						numberField("volume", "Volume", "Volume 0-100", false, 50, 50),
						numberField("rate", "Rate", "Speech rate 0.5-2.0", false, 1.0, 60),
						numberField("pitch", "Pitch", "Pitch multiplier 0.5-2.0", false, 1.0, 70),
					}},
					Formats: []string{"mp3", "wav", "pcm", "opus"},
				},
			}},
			Factory: func(config map[string]any) (sdk.SpeechProvider, error) {
				opts := []alibabaspeech.Option{}
				if v := configString(config, "api_key"); v != "" {
					opts = append(opts, alibabaspeech.WithAPIKey(v))
				}
				if v := configString(config, "base_url"); v != "" {
					opts = append(opts, alibabaspeech.WithBaseURL(v))
				}
				return alibabaspeech.New(opts...), nil
			},
			Order: 80,
		},
		{
			ClientType:  models.ClientTypeMicrosoftSpeech,
			DisplayName: "Microsoft Speech",
			Icon:        "azure-color",
			Description: "Azure Cognitive Services TTS",
			ConfigSchema: ConfigSchema{Fields: []FieldSchema{
				secretField("api_key", "API Key", "Azure speech subscription key", true, 10),
				stringField("base_url", "Base URL", "Optional full TTS endpoint override", false, "", 20),
			}},
			DefaultModel: "microsoft-tts",
			SupportsList: false,
			Models: []ModelInfo{{
				ID:          "microsoft-tts",
				Name:        "microsoft-tts",
				Description: "Default Azure speech wrapper model",
				ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					stringField("region", "Region", "Azure region, e.g. eastus", false, "eastus", 10),
					stringField("voice", "Voice", "Azure voice name", false, "en-US-JennyNeural", 20),
					stringField("language", "Language", "Optional BCP-47 language tag", false, "en-US", 30),
					stringField("output_format", "Output Format", "Azure output format", false, "audio-16khz-128kbitrate-mono-mp3", 40),
					stringField("style", "Style", "Optional speaking style", false, "", 50),
					stringField("rate", "Rate", "Optional speaking rate", false, "", 60),
					stringField("pitch", "Pitch", "Optional pitch adjustment", false, "", 70),
				}},
				Capabilities: ModelCapabilities{ConfigSchema: ConfigSchema{Fields: []FieldSchema{
					stringField("region", "Region", "Azure region, e.g. eastus", false, "eastus", 10),
					stringField("voice", "Voice", "Azure voice name", false, "en-US-JennyNeural", 20),
					stringField("language", "Language", "Optional BCP-47 language tag", false, "en-US", 30),
					stringField("output_format", "Output Format", "Azure output format", false, "audio-16khz-128kbitrate-mono-mp3", 40),
					stringField("style", "Style", "Optional speaking style", false, "", 50),
					stringField("rate", "Rate", "Optional speaking rate", false, "", 60),
					stringField("pitch", "Pitch", "Optional pitch adjustment", false, "", 70),
				}}},
			}},
			Factory: func(config map[string]any) (sdk.SpeechProvider, error) {
				opts := []microsoftspeech.Option{}
				if v := configString(config, "api_key"); v != "" {
					opts = append(opts, microsoftspeech.WithAPIKey(v))
				}
				if v := configString(config, "base_url"); v != "" {
					opts = append(opts, microsoftspeech.WithBaseURL(v))
				}
				return microsoftspeech.New(opts...), nil
			},
			Order: 90,
		},
	}
}

func stringField(key, title, description string, required bool, example any, order int) FieldSchema {
	return FieldSchema{Key: key, Type: "string", Title: title, Description: description, Required: required, Example: example, Order: order}
}

func advancedStringField(key, title, description string, required bool, example any, order int) FieldSchema {
	return FieldSchema{Key: key, Type: "string", Title: title, Description: description, Required: required, Advanced: true, Example: example, Order: order}
}

func secretField(key, title, description string, required bool, order int) FieldSchema {
	return FieldSchema{Key: key, Type: "secret", Title: title, Description: description, Required: required, Order: order}
}

func numberField(key, title, description string, required bool, example any, order int) FieldSchema {
	return FieldSchema{Key: key, Type: "number", Title: title, Description: description, Required: required, Example: example, Order: order}
}

func boolField(key, title, description string, required bool, order int) FieldSchema {
	return FieldSchema{Key: key, Type: "bool", Title: title, Description: description, Required: required, Order: order}
}

func enumField(key, title, description string, required bool, values []string, order int) FieldSchema {
	return FieldSchema{Key: key, Type: "enum", Title: title, Description: description, Required: required, Enum: values, Order: order}
}

func configString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	if v, ok := cfg[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func voiceIDs(voices []VoiceInfo) []string {
	out := make([]string, 0, len(voices))
	for _, voice := range voices {
		out = append(out, voice.ID)
	}
	return out
}
