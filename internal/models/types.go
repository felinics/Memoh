package models

import (
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/reasoning"
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
	ThinkingModeAlways       = reasoning.ModeAlways
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
	// ReasoningDialect declares the wire shape of this model's thinking control,
	// which cannot be inferred from the tiers it advertises: Gemini 2.5 takes a
	// token budget while 3.x takes a named level, and the two are mutually
	// exclusive on the same request. Declared per model because the alternative is
	// sniffing the model id, and an id is not a capability. Empty leaves provider
	// policy in charge; Google's adaptor deliberately sends no thinking control so
	// pre-dialect rows retain their safe pre-upgrade request shape.
	ReasoningDialect string `json:"reasoning_dialect,omitempty"`
	// ReasoningOffSupport declares how the model answers an explicit request to
	// stop thinking. Anthropic's per-model table splits models that share a
	// thinking mode and an identical tier list, so this cannot be derived — see the
	// reasoning package's OffSupport constants.
	ReasoningOffSupport string `json:"reasoning_off_support,omitempty"`
	// ReasoningDefaultOn reports whether omitting the thinking field leaves the
	// model thinking. Separate from off-ability: Claude 4.6 can be turned off *and*
	// defaults to off, while Opus 5 can be turned off but defaults to on, so
	// omitting the field there keeps thinking running — billed, and invisible to a
	// user who believes they turned it off. nil means unknown.
	ReasoningDefaultOn *bool `json:"reasoning_default_on,omitempty"`
	// ThinkingBudgetMin/Max bound the budget dialect. The range is per model
	// family, not per vendor: Gemini 2.5 Pro is 128..32768 and cannot be turned
	// off, while Flash starts at 0 and can.
	ThinkingBudgetMin *int `json:"thinking_budget_min,omitempty"`
	ThinkingBudgetMax *int `json:"thinking_budget_max,omitempty"`
}

func normalizeModelConfig(config ModelConfig) ModelConfig {
	if config.Description != nil {
		description := strings.TrimSpace(*config.Description)
		config.Description = &description
	}
	// Rewrites the legacy "none" spelling of off on both ModelConfig boundaries —
	// before a write is validated and after a row is read back — so nothing
	// downstream has to know that "none" was ever declarable.
	config.ReasoningEfforts = reasoning.NormalizeAdvertised(config.ReasoningEfforts)
	return config
}

// NormalizeAdvertisedEfforts forwards the catalog-boundary normalizer while
// callers migrate to the reasoning leaf package.
func NormalizeAdvertisedEfforts(efforts []string) []string {
	return reasoning.NormalizeAdvertised(efforts)
}

// ContextBudgetMaxTokens returns the configured model context window, or zero
// when budget enforcement is unavailable for this model.
func (c ModelConfig) ContextBudgetMaxTokens() int {
	if c.ContextWindow != nil && *c.ContextWindow > 0 {
		return *c.ContextWindow
	}
	return 0
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
	if !reasoning.IsValidDialect(m.Config.ReasoningDialect) {
		return errors.New("invalid reasoning dialect: " + m.Config.ReasoningDialect)
	}
	if !reasoning.IsValidOffSupport(m.Config.ReasoningOffSupport) {
		return errors.New("invalid reasoning off support: " + m.Config.ReasoningOffSupport)
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
	return reasoning.ResolveMode(m.Config.ThinkingMode, m.HasCompatibility(CompatReasoning), m.ModelID)
}

// ReasoningOptions reports what a caller may select for this model on the given
// client type: the selectable tiers, whether off is reachable, and the default.
// It is the single source every surface reads — the web picker, /reasoning, and
// the API all render this rather than deriving their own answer.
func (m *Model) ReasoningOptions(clientType string) reasoning.Options {
	mode := m.ResolveThinkingMode()
	if clientType == string(ClientTypeGoogleGenerativeAI) &&
		mode != reasoning.ModeAlways &&
		mode != reasoning.ModeNone &&
		m.Config.ReasoningDialect == "" {
		// Google generations use mutually exclusive wire fields. An imported row
		// from before the dialect schema has no safe selectable control until a
		// trusted catalog refresh backfills it, so project the model as supported
		// but uncontrollable instead of advertising a picker the adaptor ignores.
		return reasoning.Options{Supported: true}
	}
	return reasoning.OptionsFor(
		mode,
		m.Config.ReasoningEfforts,
		clientType,
		m.Config.ReasoningOffSupport,
	)
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
	// Reasoning is the model's resolved thinking options, filled by the API layer
	// (it depends on the provider's client type). Clients render this rather than
	// deriving their own answer from ThinkingMode and ReasoningEfforts — the
	// duplication that let the web picker and the wire disagree.
	Reasoning *reasoning.Options `json:"reasoning,omitempty"`
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
