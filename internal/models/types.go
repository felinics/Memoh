package models

import (
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/memohai/memoh/internal/reasoning"
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
	// CompatFileInput marks models whose provider API accepts documents (PDF)
	// as native input parts. Distinct from CompatVision: a model can have
	// vision yet lack a provider-side PDF ingestion pipeline (and vice versa
	// never occurs), so the two are routed independently.
	CompatFileInput = "file-input"
)

// Reasoning effort tokens. The vocabulary lives in internal/reasoning; these are
// forwarding aliases so existing call sites keep compiling while they migrate.
const (
	ReasoningEffortMinimal = reasoning.EffortMinimal
	ReasoningEffortLow     = reasoning.EffortLow
	ReasoningEffortMedium  = reasoning.EffortMedium
	ReasoningEffortHigh    = reasoning.EffortHigh
	ReasoningEffortXHigh   = reasoning.EffortXHigh
	ReasoningEffortMax     = reasoning.EffortMax

	// ReasoningEffortDisable is the single representation of "no reasoning" —
	// both what a user picks and what a model advertises.
	ReasoningEffortDisable = reasoning.EffortDisable
	// ReasoningEffortNone is OpenAI's wire spelling of the same state, produced
	// by adaptors and never declared or stored.
	ReasoningEffortNone = reasoning.EffortNone
)

// IsReasoningDisabled reports whether an effort value means "no reasoning".
func IsReasoningDisabled(effort string) bool {
	return reasoning.IsDisabled(effort)
}

// NearestEffortToMedium picks the tier closest to medium from levels, breaking
// ties toward the weaker tier.
func NearestEffortToMedium(levels []string) string {
	return reasoning.NearestToMedium(levels)
}

// Thinking mode tokens. Semantics live in internal/reasoning; these are
// forwarding aliases so existing call sites keep compiling while they migrate.
const (
	ThinkingModeAdaptive     = reasoning.ModeAdaptive
	ThinkingModeToggle       = reasoning.ModeToggle
	ThinkingModeOnlyAdaptive = reasoning.ModeOnlyAdaptive
	ThinkingModeNone         = reasoning.ModeNone
)

// validCompatibilities enumerates accepted compatibility tokens.
var validCompatibilities = map[string]struct{}{
	CompatVision: {}, CompatToolCall: {}, CompatImageOutput: {}, CompatReasoning: {},
	CompatFileInput: {},
}

// ValidateCompatibilities validates capability tokens supplied by a client.
func ValidateCompatibilities(compatibilities []string) error {
	for _, compatibility := range compatibilities {
		if _, ok := validCompatibilities[compatibility]; !ok {
			return errors.New("invalid compatibility: " + compatibility)
		}
	}
	return nil
}

// IsValidReasoningEffort reports whether effort can be stored in ModelConfig.
func IsValidReasoningEffort(effort string) bool {
	return reasoning.IsDeclarable(effort)
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

func normalizeModelConfig(config ModelConfig) ModelConfig {
	if config.Description != nil {
		description := strings.TrimSpace(*config.Description)
		config.Description = &description
	}
	config.ReasoningEfforts = NormalizeAdvertisedEfforts(config.ReasoningEfforts)
	return config
}

// NormalizeAdvertisedEfforts rewrites the legacy spelling of "off" to the token a
// model declares today. It runs on both boundaries of ModelConfig — before a write
// is validated and after a row is read back — and at external catalog boundaries,
// so nothing downstream has to know that "none" was ever declarable.
//
// Without it the vocabulary change would only apply to freshly written configs:
// rows persisted earlier, and provider registries that have not been regenerated,
// would keep advertising "none", and every consumer that now looks for the disable
// token would read those models as "cannot be turned off" — silently dropping Off
// from the picker and misreading which thinking mechanism the model wants.
func NormalizeAdvertisedEfforts(efforts []string) []string {
	return reasoning.NormalizeAdvertised(efforts)
}

type Model struct {
	ModelID    string      `json:"model_id"`
	Name       string      `json:"name"`
	ProviderID string      `json:"provider_id"`
	Type       ModelType   `json:"type"`
	Enable     bool        `json:"enable"`
	Config     ModelConfig `json:"config"`
}

// ResolveEnable returns the effective enable flag: when the override is nil,
// the current value is preserved; otherwise the override wins. Used by
// Service.Create (current=true default) and Service.UpdateByID (current=stored).
func ResolveEnable(override *bool, current bool) bool {
	if override == nil {
		return current
	}
	return *override
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
	if err := ValidateCompatibilities(m.Config.Compatibilities); err != nil {
		return err
	}
	for _, effort := range m.Config.ReasoningEfforts {
		if !IsValidReasoningEffort(effort) {
			return errors.New("invalid reasoning effort: " + effort)
		}
	}
	if m.Config.ThinkingMode != "" && !reasoning.IsValidMode(m.Config.ThinkingMode) {
		return errors.New("invalid thinking mode: " + m.Config.ThinkingMode)
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

// ResolveThinkingMode returns the effective ThinkingMode, bridging legacy data:
// unknown + reasoning compat → toggle; unknown without it → none.
func (m *Model) ResolveThinkingMode() string {
	return reasoning.ResolveMode(m.Config.ThinkingMode, m.HasCompatibility(CompatReasoning))
}

// ReasoningOptions reports what a caller may select for this model on the given
// client type: the selectable tiers, whether off is reachable, and the default.
// It is the single source every surface reads — the web picker, /reasoning, and
// the API all render this rather than deriving their own answer.
func (m *Model) ReasoningOptions(clientType string) reasoning.Options {
	return reasoning.OptionsFor(m.ResolveThinkingMode(), m.Config.ReasoningEfforts, clientType)
}

// AddRequest is the payload for creating a new model. Enable is a pointer so
// the server can default to true when the field is absent from the request.
type AddRequest struct {
	ModelID    string      `json:"model_id"`
	Name       string      `json:"name,omitempty"`
	ProviderID string      `json:"provider_id"`
	Type       ModelType   `json:"type"`
	Enable     *bool       `json:"enable,omitempty"`
	Config     ModelConfig `json:"config"`
}

type AddResponse struct {
	ID      string `json:"id"`
	ModelID string `json:"model_id"`
}

type GetRequest struct {
	ID string `json:"id"`
}

type GetResponse struct {
	ID      string `json:"id"`
	ModelID string `json:"model_id"`
	Model
}

// UpdateRequest is the payload for updating an existing model. Enable is a
// pointer so callers can omit it to preserve the current enable state while
// still rewriting the other fields.
type UpdateRequest struct {
	ModelID    string      `json:"model_id"`
	Name       string      `json:"name,omitempty"`
	ProviderID string      `json:"provider_id"`
	Type       ModelType   `json:"type"`
	Enable     *bool       `json:"enable,omitempty"`
	Config     ModelConfig `json:"config"`
}

// toModel builds a Model from an AddRequest using the given enable value.
func (r AddRequest) toModel(enable bool) Model {
	return Model{
		ModelID:    r.ModelID,
		Name:       r.Name,
		ProviderID: r.ProviderID,
		Type:       r.Type,
		Enable:     enable,
		Config:     r.Config,
	}
}

// toModel builds a Model from an UpdateRequest using the given enable value.
func (r UpdateRequest) toModel(enable bool) Model {
	return Model{
		ModelID:    r.ModelID,
		Name:       r.Name,
		ProviderID: r.ProviderID,
		Type:       r.Type,
		Enable:     enable,
		Config:     r.Config,
	}
}

type ListRequest struct {
	Type ModelType `json:"type,omitempty"`
}

type DeleteRequest struct {
	ID      string `json:"id,omitempty"`
	ModelID string `json:"model_id,omitempty"`
}

type DeleteResponse struct {
	Message string `json:"message"`
}

type CountResponse struct {
	Count int64 `json:"count"`
}

// TestStatus represents the outcome of probing a model.
type TestStatus string

const (
	TestStatusOK                TestStatus = "ok"
	TestStatusAuthError         TestStatus = "auth_error"
	TestStatusModelNotSupported TestStatus = "model_not_supported"
	TestStatusError             TestStatus = "error"
)

// TestResponse is returned by POST /models/:id/test.
type TestResponse struct {
	Status    TestStatus `json:"status"`
	Reachable bool       `json:"reachable"`
	LatencyMs int64      `json:"latency_ms,omitempty"`
	Message   string     `json:"message,omitempty"`
}
