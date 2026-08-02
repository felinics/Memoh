package models

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

// ReasoningEffortDisable turns reasoning off. It is not a wire tier a model can
// advertise — it is the settings/override value that says "send no reasoning at
// all", which is why IsValidReasoningEffort rejects it. Since bots dropped the
// separate reasoning_enabled flag, this is the only representation of "off".
const ReasoningEffortDisable = "disable"

// orderedReasoningEfforts lists the real tiers weakest to strongest. Order is
// what NearestEffortToMedium walks, so it must stay monotonic.
var orderedReasoningEfforts = []string{
	ReasoningEffortNone,
	ReasoningEffortMinimal,
	ReasoningEffortLow,
	ReasoningEffortMedium,
	ReasoningEffortHigh,
	ReasoningEffortXHigh,
	ReasoningEffortMax,
}

// IsReasoningDisabled reports whether an effort value means "no reasoning".
func IsReasoningDisabled(effort string) bool {
	return strings.TrimSpace(effort) == ReasoningEffortDisable
}

// NearestEffortToMedium picks the tier closest to medium from levels, breaking
// ties toward the weaker tier. It is the fallback when a model does not
// advertise medium: [minimal low] -> low, [high max] -> high, [low high] -> low.
// Ignores values outside the known tier list (including "disable"), and returns
// "" when levels has no usable tier.
//
// Keep in sync with nearestEffortToMedium in
// apps/web/src/pages/bots/components/reasoning-effort.ts.
func NearestEffortToMedium(levels []string) string {
	mediumIdx := -1
	for i, e := range orderedReasoningEfforts {
		if e == ReasoningEffortMedium {
			mediumIdx = i
			break
		}
	}

	best, bestIdx, bestDistance := "", 0, 0
	for _, level := range levels {
		idx := -1
		for i, e := range orderedReasoningEfforts {
			if e == level {
				idx = i
				break
			}
		}
		if idx < 0 {
			continue
		}
		distance := idx - mediumIdx
		if distance < 0 {
			distance = -distance
		}
		// Ties break toward the weaker tier (smaller index) rather than toward
		// whichever came first, because levels arrives in registry order and is
		// not guaranteed to be sorted.
		if best == "" || distance < bestDistance || (distance == bestDistance && idx < bestIdx) {
			best, bestIdx, bestDistance = level, idx, distance
		}
	}
	return best
}

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

func normalizeModelConfig(config ModelConfig) ModelConfig {
	if config.Description != nil {
		description := strings.TrimSpace(*config.Description)
		config.Description = &description
	}
	return config
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
