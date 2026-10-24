package models

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/reasoning"
)

// FieldError is a model write rejected because of one request field. Field is
// the name the request uses for it, with dots for a nested key; Required says
// the field is missing or empty rather than holding a bad value. Err is the
// private reason.
type FieldError struct {
	Field    string
	Required bool
	Err      error
}

func (e *FieldError) Error() string {
	if e.Err == nil {
		return e.Field
	}
	return e.Field + ": " + e.Err.Error()
}

func (e *FieldError) Unwrap() error { return e.Err }

func fieldRequired(field string) error {
	return &FieldError{Field: field, Required: true, Err: errors.New("is required")}
}

func fieldInvalid(field string, cause error) error {
	return &FieldError{Field: field, Err: cause}
}

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
	ClientTypeOpenCodeGo              ClientType = "opencode-go"
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
	ClientTypeAlibabaTranscription    ClientType = "alibabacloud-transcription"
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
	ThinkingModeAdaptive = reasoning.ModeAdaptive
	ThinkingModeToggle   = reasoning.ModeToggle
	ThinkingModeAlways   = reasoning.ModeAlways
	ThinkingModeNone     = reasoning.ModeNone
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

// ModelConfigSchemaVersion is the shape every ModelConfig is written in. A
// stored config without schema_version is version 1; config_migration.go turns
// older versions into this one when a row is read, and the next write persists
// the result. There is no database migration.
const ModelConfigSchemaVersion = 2

// Modality is a kind of content a model accepts or produces. The values match
// Twilight's sdk.Modality.
type Modality string

const (
	ModalityText  Modality = "text"
	ModalityImage Modality = "image"
	ModalityAudio Modality = "audio"
	ModalityVideo Modality = "video"
	ModalityPDF   Modality = "pdf"
)

var validModalities = map[Modality]struct{}{
	ModalityText: {}, ModalityImage: {}, ModalityAudio: {}, ModalityVideo: {}, ModalityPDF: {},
}

// ModelModalities lists what a model accepts and produces.
type ModelModalities struct {
	Input  []Modality `json:"input,omitempty"`
	Output []Modality `json:"output,omitempty"`
}

// ModelReasoning mirrors Twilight's sdk.ModelReasoning: the reasoning controls
// a model exposes (Toggle, Efforts, Budget, Off), the request shape that
// carries them (Dialect), what the model does when asked for nothing
// (Default), and how a Chat Completions host reads reasoning back (Replay).
type ModelReasoning struct {
	// Toggle reports a request field that turns reasoning on and off, separate
	// from any effort level.
	Toggle bool `json:"toggle,omitempty"`
	// Efforts lists the effort levels the model accepts, weakest to strongest.
	// Turning reasoning off is Off, not an effort.
	Efforts []string `json:"efforts,omitempty"`
	// Budget is the reasoning token budget range; nil means no budget control.
	Budget *ReasoningBudget `json:"budget,omitempty"`
	// Off is how the model answers a request to stop reasoning: accepted,
	// low_effort_only, rejected, or empty when not stated.
	Off string `json:"off,omitempty"`
	// Dialect is the request shape that carries the controls: tier, budget,
	// toggle, adaptive, or empty when not stated.
	Dialect string `json:"dialect,omitempty"`
	// Default is what the model does when a request sets no reasoning control.
	Default *ReasoningDefault `json:"default,omitempty"`
	// Replay is how a Chat Completions host reads reasoning back on replayed
	// messages; nil leaves the provider's compatibility mode in charge.
	Replay *ReasoningReplay `json:"replay,omitempty"`
}

// ReasoningBudget bounds a reasoning token budget. A nil bound is not stated.
type ReasoningBudget struct {
	Min *int `json:"min,omitempty"`
	Max *int `json:"max,omitempty"`
}

// ReasoningDefault is what a model does when a request sets no reasoning
// control. A zero field is not stated.
type ReasoningDefault struct {
	Enabled *bool  `json:"enabled,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Budget  *int   `json:"budget,omitempty"`
}

// ReasoningReplay is the assistant message field that carries a step's
// reasoning back to a Chat Completions host, and whether every tool call
// replay must carry it.
type ReasoningReplay struct {
	Field    string `json:"field,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// Reasoning replay fields, matching Twilight's sdk.ReasoningReplayField.
const (
	ReasoningReplayContent   = "reasoning_content"
	ReasoningReplayDetails   = "reasoning_details"
	ReasoningReplayReasoning = "reasoning"
)

// ModelConfig holds the JSONB config stored per model, in the shape of
// Twilight's sdk.Model and sdk.EmbeddingModel. Chat models use Modalities,
// ContextWindow, MaxOutputTokens, ToolCall, StructuredOutput and Reasoning;
// embedding models use Dimensions.
type ModelConfig struct {
	// SchemaVersion is ModelConfigSchemaVersion on every config this version
	// writes.
	SchemaVersion int     `json:"schema_version"`
	Description   *string `json:"description,omitempty"`

	Modalities *ModelModalities `json:"modalities,omitempty"`
	// ContextWindow is the context window in tokens, input and output together.
	ContextWindow *int `json:"context_window,omitempty"`
	// MaxOutputTokens is the most output tokens the model can produce in one
	// response; nil means unknown. When set, every turn requests an explicit
	// limit within it.
	MaxOutputTokens *int `json:"max_output_tokens,omitempty"`
	ToolCall        bool `json:"tool_call,omitempty"`
	// StructuredOutput reports support for a JSON schema response format.
	StructuredOutput bool `json:"structured_output,omitempty"`
	// Reasoning is nil when the model does not reason.
	Reasoning *ModelReasoning `json:"reasoning,omitempty"`

	// Dimensions is the embedding vector size.
	Dimensions *int `json:"dimensions,omitempty"`

	// CatalogAvailable is false when a managed catalog stopped listing the model.
	CatalogAvailable *bool `json:"catalog_available,omitempty"`
}

// MarshalJSON always writes the current schema version, so a config built
// without one is never read back as version 1.
func (c ModelConfig) MarshalJSON() ([]byte, error) {
	type plain ModelConfig
	c.SchemaVersion = ModelConfigSchemaVersion
	return json.Marshal(plain(c))
}

// Accepts reports whether the model takes the modality as input.
func (c ModelConfig) Accepts(modality Modality) bool {
	return c.Modalities != nil && slices.Contains(c.Modalities.Input, modality)
}

// Produces reports whether the model returns the modality as output.
func (c ModelConfig) Produces(modality Modality) bool {
	return c.Modalities != nil && slices.Contains(c.Modalities.Output, modality)
}

func normalizeModelConfig(config ModelConfig) ModelConfig {
	config.SchemaVersion = ModelConfigSchemaVersion
	if config.Description != nil {
		description := strings.TrimSpace(*config.Description)
		config.Description = &description
	}
	if config.Modalities != nil {
		config.Modalities = &ModelModalities{
			Input:  dedupeModalities(config.Modalities.Input),
			Output: dedupeModalities(config.Modalities.Output),
		}
		if len(config.Modalities.Input) == 0 && len(config.Modalities.Output) == 0 {
			config.Modalities = nil
		}
	}
	if config.Reasoning != nil {
		r := *config.Reasoning
		r.Efforts = dedupeStrings(r.Efforts)
		if r.Budget != nil && r.Budget.Min == nil && r.Budget.Max == nil {
			r.Budget = nil
		}
		if r.Default != nil && r.Default.Enabled == nil && r.Default.Effort == "" && r.Default.Budget == nil {
			r.Default = nil
		}
		config.Reasoning = &r
	}
	return config
}

func dedupeModalities(in []Modality) []Modality {
	if len(in) == 0 {
		return nil
	}
	out := make([]Modality, 0, len(in))
	for _, m := range in {
		m = Modality(strings.TrimSpace(string(m)))
		if m != "" && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
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

// MaxOutputTokensLimit returns the configured maximum output tokens, or zero
// when the model's cap is unknown.
func (c ModelConfig) MaxOutputTokensLimit() int {
	if c.MaxOutputTokens != nil && *c.MaxOutputTokens > 0 {
		return *c.MaxOutputTokens
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

// Validate checks a model write. Every failure is a *FieldError naming the
// request field that holds the offending value.
func (m *Model) Validate() error {
	if m.ModelID == "" {
		return fieldRequired("model_id")
	}
	if m.ProviderID == "" {
		return fieldRequired("provider_id")
	}
	if _, err := uuid.Parse(m.ProviderID); err != nil {
		return fieldInvalid("provider_id", err)
	}
	if !IsValidModelType(m.Type) {
		return fieldInvalid("type", ErrInvalidModelType)
	}
	return m.Config.Validate(m.Type)
}

// Validate checks configuration values for a resolved model type. Catalogs can
// validate configs before a tenant provider or database model exists.
func (c ModelConfig) Validate(modelType ModelType) error {
	if c.MaxOutputTokens != nil && *c.MaxOutputTokens <= 0 {
		return errors.New("max output tokens must be greater than 0")
	}
	if modelType == ModelTypeEmbedding {
		if c.Dimensions == nil || *c.Dimensions <= 0 {
			return fieldRequired("config.dimensions")
		}
	}
	if c.Modalities != nil {
		for _, modality := range append(slices.Clone(c.Modalities.Input), c.Modalities.Output...) {
			if _, ok := validModalities[modality]; !ok {
				return fieldInvalid("config.modalities", errors.New("invalid modality: "+string(modality)))
			}
		}
	}
	if err := validateModelReasoning(c.Reasoning); err != nil {
		return err
	}
	return nil
}

func validateModelReasoning(r *ModelReasoning) error {
	if r == nil {
		return nil
	}
	for _, effort := range r.Efforts {
		if !slices.Contains(reasoning.OrderedEfforts(), effort) {
			return fieldInvalid("config.reasoning.efforts", errors.New("invalid reasoning effort: "+effort))
		}
	}
	if !reasoning.IsValidDialect(r.Dialect) {
		return fieldInvalid("config.reasoning.dialect", errors.New("invalid reasoning dialect: "+r.Dialect))
	}
	if !reasoning.IsValidOffSupport(r.Off) {
		return fieldInvalid("config.reasoning.off", errors.New("invalid reasoning off support: "+r.Off))
	}
	if r.Budget != nil {
		if r.Budget.Min != nil && *r.Budget.Min < 0 {
			return fieldInvalid("config.reasoning.budget.min", errors.New("must not be negative"))
		}
		if r.Budget.Max != nil && *r.Budget.Max <= 0 {
			return fieldInvalid("config.reasoning.budget.max", errors.New("must be greater than 0"))
		}
		if r.Budget.Min != nil && r.Budget.Max != nil && *r.Budget.Min > *r.Budget.Max {
			return fieldInvalid("config.reasoning.budget", errors.New("min must not exceed max"))
		}
	}
	if r.Default != nil && r.Default.Effort != "" && !slices.Contains(reasoning.OrderedEfforts(), r.Default.Effort) {
		return fieldInvalid("config.reasoning.default.effort", errors.New("invalid reasoning effort: "+r.Default.Effort))
	}
	if r.Default != nil && r.Default.Budget != nil && *r.Default.Budget < 0 {
		return fieldInvalid("config.reasoning.default.budget", errors.New("must not be negative"))
	}
	if r.Replay != nil {
		switch r.Replay.Field {
		case "", ReasoningReplayContent, ReasoningReplayDetails, ReasoningReplayReasoning:
		default:
			return fieldInvalid("config.reasoning.replay.field", errors.New("invalid reasoning replay field: "+r.Replay.Field))
		}
	}
	return nil
}

// HasCompatibility reports whether the model has a capability; see
// ModelConfig.Supports.
func (m *Model) HasCompatibility(c string) bool {
	return m.Config.Supports(c)
}

// ResolveThinkingMode returns the model's thinking mode.
func (m *Model) ResolveThinkingMode() string {
	return m.Config.ThinkingMode()
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
		m.Config.ReasoningDialect() == "" {
		// Google generations use mutually exclusive wire fields. An imported row
		// from before the dialect schema has no safe selectable control until a
		// trusted catalog refresh backfills it, so project the model as supported
		// but uncontrollable instead of advertising a picker the adaptor ignores.
		return reasoning.Options{Supported: true}
	}
	return withDeclaredDefaults(reasoning.OptionsFor(
		mode,
		m.Config.ReasoningEfforts(),
		clientType,
		m.Config.ReasoningOffSupport(),
	), m.Config.Reasoning)
}

// withDeclaredDefaults makes the model's declared defaults the options'
// default selection: the tier a client starts on when the model is picked, and
// whether it starts off. A default the options cannot reach, a tier not
// offered or off on a model that cannot turn off, is ignored.
func withDeclaredDefaults(opts reasoning.Options, r *ModelReasoning) reasoning.Options {
	if r == nil || r.Default == nil || !opts.Supported {
		return opts
	}
	if effort := r.Default.Effort; effort != "" && slices.Contains(opts.Efforts, effort) {
		opts.DefaultEffort = effort
	}
	if enabled := r.Default.Enabled; enabled != nil && !*enabled && opts.CanDisable {
		opts.DefaultOff = true
	}
	return opts
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
	// rawConfig is the config as the client sent it. Its decode cannot see the
	// provider, so the service reads it again when the upgrade needs the
	// provider's client type; see RequestConfigNeedsClientType.
	rawConfig json.RawMessage
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

// MarshalJSON adds the version 1 reading of a chat model's config next to its
// current fields. Clients built before version 2 (an older desktop build, a
// cached web bundle) read capabilities and efforts only from those keys, and
// copy them back when they save, which DecodeRequestModelConfig reads as their
// edit (see mergeLegacyEdit). Current clients ignore the keys and drop them
// before saving. The keys exist only in API responses, never in the row.
func (r GetResponse) MarshalJSON() ([]byte, error) {
	type plain GetResponse
	raw, err := json.Marshal(plain(r))
	if err != nil || r.Type != ModelTypeChat {
		return raw, err
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	config := map[string]json.RawMessage{}
	if err := json.Unmarshal(body["config"], &config); err != nil {
		return nil, err
	}
	legacy, err := json.Marshal(r.Config.Legacy())
	if err != nil {
		return nil, err
	}
	view := map[string]json.RawMessage{}
	if err := json.Unmarshal(legacy, &view); err != nil {
		return nil, err
	}
	for _, key := range legacyOnlyKeys {
		if value, ok := view[key]; ok {
			config[key] = value
		}
	}
	if body["config"], err = json.Marshal(config); err != nil {
		return nil, err
	}
	return json.Marshal(body)
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
	// rawConfig is the config as the client sent it; see AddRequest.rawConfig.
	rawConfig json.RawMessage
}

// UnmarshalJSON reads config in any schema version; see
// DecodeRequestModelConfig.
func (r *AddRequest) UnmarshalJSON(data []byte) error {
	type plain AddRequest
	var body struct {
		plain
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return err
	}
	*r = AddRequest(body.plain)
	config, err := decodeRequestConfig(body.Config, r.ModelID, r.Type)
	if err != nil {
		return err
	}
	r.Config = config
	r.rawConfig = body.Config
	return nil
}

// UnmarshalJSON reads config in any schema version; see
// DecodeRequestModelConfig.
func (r *UpdateRequest) UnmarshalJSON(data []byte) error {
	type plain UpdateRequest
	var body struct {
		plain
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return err
	}
	*r = UpdateRequest(body.plain)
	config, err := decodeRequestConfig(body.Config, r.ModelID, r.Type)
	if err != nil {
		return err
	}
	r.Config = config
	r.rawConfig = body.Config
	return nil
}

func decodeRequestConfig(raw json.RawMessage, modelID string, modelType ModelType) (ModelConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return ModelConfig{}, nil
	}
	return DecodeRequestModelConfig(raw, ConfigContext{ModelID: modelID, Type: modelType})
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
	// Code is the error catalog code for why the test did not pass. It is
	// empty on success and when no catalog code describes the failure.
	Code string `json:"code,omitempty"`
	// Cause is why the test did not pass. It never leaves the process; the
	// handler records it and derives Code from it.
	Cause error `json:"-"`
}
