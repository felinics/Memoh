package model

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

type ModelType string

const (
	ModelTypeChat          ModelType = "chat"
	ModelTypeEmbedding     ModelType = "embedding"
	ModelTypeSpeech        ModelType = "speech"
	ModelTypeTranscription ModelType = "transcription"
	ModelTypeVideo         ModelType = "video"
)

type ClientType string

const (
	ClientTypeOpenAIResponses         ClientType = "openai-responses"
	ClientTypeOpenAICompletions       ClientType = "openai-completions"
	ClientTypeAnthropicMessages       ClientType = "anthropic-messages"
	ClientTypeGoogleGenerativeAI      ClientType = "google-generative-ai"
	ClientTypeOpenAICodex             ClientType = "openai-codex"
	ClientTypeGitHubCopilot           ClientType = "github-copilot"
	ClientTypeEdgeSpeech              ClientType = "edge-speech"
	ClientTypeOpenAISpeech            ClientType = "openai-speech"
	ClientTypeOpenAITranscription     ClientType = "openai-transcription"
	ClientTypeOpenRouterSpeech        ClientType = "openrouter-speech"
	ClientTypeOpenRouterTranscription ClientType = "openrouter-transcription"
	ClientTypeElevenLabsSpeech        ClientType = "elevenlabs-speech"
	ClientTypeElevenLabsTranscription ClientType = "elevenlabs-transcription"
	ClientTypeDeepgramSpeech          ClientType = "deepgram-speech"
	ClientTypeDeepgramTranscription   ClientType = "deepgram-transcription"
	ClientTypeMiniMaxSpeech           ClientType = "minimax-speech"
	ClientTypeVolcengineSpeech        ClientType = "volcengine-speech"
	ClientTypeAlibabaSpeech           ClientType = "alibabacloud-speech"
	ClientTypeMicrosoftSpeech         ClientType = "microsoft-speech"
	ClientTypeGoogleSpeech            ClientType = "google-speech"
	ClientTypeGoogleTranscription     ClientType = "google-transcription"
	ClientTypeOpenRouterVideo         ClientType = "openrouter-video"
	ClientTypeModelArkVideo           ClientType = "modelark-video"
	ClientTypeVolcengineVideo         ClientType = "volcengine-video"
)

const (
	CompatVision      = "vision"
	CompatToolCall    = "tool-call"
	CompatImageOutput = "image-output"
	CompatReasoning   = "reasoning"
)

const (
	ReasoningEffortNone    = "none"
	ReasoningEffortMinimal = "minimal"
	ReasoningEffortLow     = "low"
	ReasoningEffortMedium  = "medium"
	ReasoningEffortHigh    = "high"
	ReasoningEffortXHigh   = "xhigh"
	ReasoningEffortMax     = "max"
)

// ThinkingMode describes how a model's extended-thinking control behaves. It is
// the capability-discovery output that the UI and wire layer key off of.
//
//   - toggle:        user can turn thinking on/off (most reasoning/hybrid models,
//     incl. OpenAI). "off" wire behavior is provider-specific (see adapter).
//   - adaptive:      user can turn thinking on/off; when on, the provider uses
//     adaptive thinking (Claude 4.6+/4.7/4.8).
//   - only_adaptive: legacy alias for adaptive retained for branch-local imports.
//   - none:          model has no thinking concept.
//
// An empty value means "unknown" and is treated as a transitional state that
// falls back to the legacy CompatReasoning flag (see Model.SupportsReasoning).
const (
	ThinkingModeAdaptive     = "adaptive"
	ThinkingModeToggle       = "toggle"
	ThinkingModeOnlyAdaptive = "only_adaptive"
	ThinkingModeNone         = "none"
)

// validCompatibilities enumerates accepted compatibility tokens.
var validCompatibilities = map[string]struct{}{
	CompatVision: {}, CompatToolCall: {}, CompatImageOutput: {}, CompatReasoning: {},
}

var validReasoningEfforts = map[string]struct{}{
	ReasoningEffortNone:    {},
	ReasoningEffortMinimal: {},
	ReasoningEffortLow:     {},
	ReasoningEffortMedium:  {},
	ReasoningEffortHigh:    {},
	ReasoningEffortXHigh:   {},
	ReasoningEffortMax:     {},
}

// IsValidReasoningEffort reports whether effort can be stored in ModelConfig.
func IsValidReasoningEffort(effort string) bool {
	_, ok := validReasoningEfforts[effort]
	return ok
}

var validThinkingModes = map[string]struct{}{
	ThinkingModeAdaptive:     {},
	ThinkingModeToggle:       {},
	ThinkingModeOnlyAdaptive: {},
	ThinkingModeNone:         {},
}

// ModelConfig holds the JSONB config stored per model.
//
// ReasoningEfforts is the model's effort-level list (a.k.a. effort_levels in the
// design doc); the JSON key stays "reasoning_efforts" for backward compatibility.
// ThinkingMode is the discovered thinking behavior; empty = unknown (legacy data),
// resolved via SupportsReasoning / ResolveThinkingMode.
type ModelConfig struct {
	Description      *string  `json:"description,omitempty"`
	Dimensions       *int     `json:"dimensions,omitempty"`
	Compatibilities  []string `json:"compatibilities,omitempty"`
	ContextWindow    *int     `json:"context_window,omitempty"`
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
	ThinkingMode     string   `json:"thinking_mode,omitempty"`
	CatalogAvailable *bool    `json:"catalog_available,omitempty"`
}

type Model struct {
	ModelID    string      `json:"model_id"`
	Name       string      `json:"name"`
	ProviderID string      `json:"provider_id"`
	Type       ModelType   `json:"type"`
	Enable     bool        `json:"enable"`
	Config     ModelConfig `json:"config"`
}

func (m *Model) Validate() error {
	if m.ModelID == "" {
		return errors.New("model ID is required")
	}
	if m.ProviderID == "" {
		return errors.New("provider ID is required")
	}
	if _, err := uuid.Parse(m.ProviderID); err != nil {
		return errors.New("provider ID must be a valid UUID")
	}
	if !IsValidModelType(m.Type) {
		return errors.New("invalid model type")
	}
	if m.Type == ModelTypeEmbedding {
		if m.Config.Dimensions == nil || *m.Config.Dimensions <= 0 {
			return errors.New("dimensions must be greater than 0 for embedding models")
		}
	}
	for _, c := range m.Config.Compatibilities {
		if _, ok := validCompatibilities[c]; !ok {
			return errors.New("invalid compatibility: " + c)
		}
	}
	for _, effort := range m.Config.ReasoningEfforts {
		if !IsValidReasoningEffort(effort) {
			return errors.New("invalid reasoning effort: " + effort)
		}
	}
	if m.Config.ThinkingMode != "" {
		if _, ok := validThinkingModes[m.Config.ThinkingMode]; !ok {
			return errors.New("invalid thinking mode: " + m.Config.ThinkingMode)
		}
	}
	return nil
}

// HasCompatibility checks whether the model config includes the given capability.
func (m *Model) HasCompatibility(c string) bool {
	for _, v := range m.Config.Compatibilities {
		if v == c {
			return true
		}
	}
	return false
}

// SupportsReasoning reports whether the model supports extended thinking. It
// prefers the new ThinkingMode field and falls back to the legacy
// CompatReasoning flag for models discovered before the thinking-mode schema
// existed (transitional; resolved naturally on next model re-fetch).
func (m *Model) SupportsReasoning() bool {
	switch m.Config.ThinkingMode {
	case ThinkingModeToggle, ThinkingModeAdaptive, ThinkingModeOnlyAdaptive:
		return true
	case ThinkingModeNone:
		return false
	default: // unknown / empty → legacy bridge
		return m.HasCompatibility(CompatReasoning)
	}
}

// ResolveThinkingMode returns the effective ThinkingMode, bridging legacy data:
// unknown + reasoning compat → toggle; unknown without it → none.
func (m *Model) ResolveThinkingMode() string {
	switch m.Config.ThinkingMode {
	case ThinkingModeToggle, ThinkingModeAdaptive, ThinkingModeNone:
		return m.Config.ThinkingMode
	case ThinkingModeOnlyAdaptive:
		return ThinkingModeAdaptive
	default:
		if m.HasCompatibility(CompatReasoning) {
			return ThinkingModeToggle
		}
		return ThinkingModeNone
	}
}

// IsValidClientType returns true if the given client type is supported.
func IsValidClientType(clientType ClientType) bool {
	switch clientType {
	case ClientTypeOpenAIResponses,
		ClientTypeOpenAICompletions,
		ClientTypeAnthropicMessages,
		ClientTypeGoogleGenerativeAI,
		ClientTypeOpenAICodex,
		ClientTypeGitHubCopilot,
		ClientTypeEdgeSpeech,
		ClientTypeOpenAISpeech,
		ClientTypeOpenAITranscription,
		ClientTypeOpenRouterSpeech,
		ClientTypeOpenRouterTranscription,
		ClientTypeElevenLabsSpeech,
		ClientTypeElevenLabsTranscription,
		ClientTypeDeepgramSpeech,
		ClientTypeDeepgramTranscription,
		ClientTypeMiniMaxSpeech,
		ClientTypeVolcengineSpeech,
		ClientTypeAlibabaSpeech,
		ClientTypeMicrosoftSpeech,
		ClientTypeGoogleSpeech,
		ClientTypeGoogleTranscription,
		ClientTypeOpenRouterVideo,
		ClientTypeModelArkVideo,
		ClientTypeVolcengineVideo:
		return true
	default:
		return false
	}
}

func IsValidModelType(modelType ModelType) bool {
	switch modelType {
	case ModelTypeChat, ModelTypeEmbedding, ModelTypeSpeech, ModelTypeTranscription, ModelTypeVideo:
		return true
	default:
		return false
	}
}

// IsLLMClientType returns true if the client type belongs to the LLM domain
// (chat/embedding), excluding speech-only types (any type ending in "-speech").
func IsLLMClientType(clientType ClientType) bool {
	return IsValidClientType(clientType) &&
		!strings.HasSuffix(string(clientType), "-speech") &&
		!strings.HasSuffix(string(clientType), "-transcription") &&
		!strings.HasSuffix(string(clientType), "-video")
}
