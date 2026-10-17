package models

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/felinics/memoh/internal/reasoning"
)

// Stored model configs carry schema_version; a config without one is version
// 1. Rows are not migrated in the database. DecodeModelConfig upgrades a config
// when it is read, and the next write stores the current version, so a row
// converts the first time someone uses it.
//
// To add a version, raise ModelConfigSchemaVersion, keep the previous shape as
// a private type here, and append its upgrade to configMigrations.

// ConfigContext is what an upgrade may need beyond the config itself. Version
// 1 left the thinking mode empty on rows imported before it existed, and the
// mode was then inferred from the model id.
type ConfigContext struct {
	ModelID string
	Type    ModelType
	// ClientType is the provider's client type when the caller knows it. The
	// version 1 upgrade reads it to keep a switch that only the protocol could
	// turn off resolving as it did; empty leaves that answer as not stated.
	ClientType string
}

// configMigrations[v] upgrades a version v config to version v+1.
var configMigrations = map[int]func(json.RawMessage, ConfigContext) (json.RawMessage, error){
	1: migrateModelConfigV1,
}

// DecodeModelConfig reads a stored config of any schema version into the
// current shape. Empty input decodes to an empty config. A version newer than
// this build decodes as the current one, keeping the fields it knows.
func DecodeModelConfig(raw []byte, ctx ConfigContext) (ModelConfig, error) {
	data := json.RawMessage(raw)
	if len(strings.TrimSpace(string(data))) == 0 || string(data) == "null" {
		return normalizeModelConfig(ModelConfig{}), nil
	}
	var head struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return ModelConfig{}, fmt.Errorf("decode model config: %w", err)
	}
	version := head.SchemaVersion
	if version < 1 {
		version = 1
	}
	for ; version < ModelConfigSchemaVersion; version++ {
		migrate, ok := configMigrations[version]
		if !ok {
			return ModelConfig{}, fmt.Errorf("decode model config: no migration from schema version %d", version)
		}
		next, err := migrate(data, ctx)
		if err != nil {
			return ModelConfig{}, fmt.Errorf("migrate model config from schema version %d: %w", version, err)
		}
		data = next
	}
	var config ModelConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return ModelConfig{}, fmt.Errorf("decode model config: %w", err)
	}
	return normalizeModelConfig(config), nil
}

// storedConfigVersion reads the schema version a stored config was written
// in; a config without one, or empty input, is version 1.
func storedConfigVersion(raw []byte) int {
	var head struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil || head.SchemaVersion < 1 {
		return 1
	}
	return head.SchemaVersion
}

// upgradeNeedsClientType reports whether the version 1 upgrade of a stored
// config reads the provider's client type: only a switch advertised by the
// enabled token alone, whose off answer depends on the protocol. Every other
// row upgrades the same on every protocol, so callers skip the lookup.
func upgradeNeedsClientType(raw []byte) bool {
	if storedConfigVersion(raw) >= ModelConfigSchemaVersion {
		return false
	}
	var head struct {
		ReasoningEfforts []string `json:"reasoning_efforts"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return false
	}
	advertised := reasoning.NormalizeAdvertised(head.ReasoningEfforts)
	return slices.Contains(advertised, reasoning.EffortEnabled) && !slices.Contains(advertised, reasoning.EffortDisable)
}

// configNeedsWriteBack reports whether a stored config is older than the
// current version and belongs to a model whose config is a ModelConfig. Speech,
// transcription and video models keep their own settings in the same column,
// which ModelConfig does not describe, so they are never rewritten.
func configNeedsWriteBack(raw []byte, modelType ModelType) bool {
	if modelType != ModelTypeChat && modelType != ModelTypeEmbedding {
		return false
	}
	if len(strings.TrimSpace(string(raw))) == 0 || string(raw) == "null" {
		return false
	}
	return storedConfigVersion(raw) < ModelConfigSchemaVersion
}

// encodeUpgradedConfig writes config over the stored object: version 1 keys
// are dropped, current keys replace theirs, and keys this version does not
// know are kept for whoever wrote them.
func encodeUpgradedConfig(raw []byte, config ModelConfig) ([]byte, error) {
	stored := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, err
	}
	if stored == nil {
		stored = map[string]json.RawMessage{}
	}
	for _, key := range legacyOnlyKeys {
		delete(stored, key)
	}
	current, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(current, &fields); err != nil {
		return nil, err
	}
	for key, value := range fields {
		stored[key] = value
	}
	return json.Marshal(stored)
}

// legacyOnlyKeys are version 1 keys that version 2 does not use.
var legacyOnlyKeys = []string{
	"compatibilities", "reasoning_efforts", "thinking_mode", "reasoning_dialect",
	"reasoning_off_support", "reasoning_default_on", "thinking_budget_min", "thinking_budget_max",
}

// DecodeRequestModelConfig reads a config sent by an API client. A stored
// config without schema_version is version 1, but a client may send the
// current shape and leave the version out, so a request without one is read as
// version 1 only when it uses a key that only version 1 has.
func DecodeRequestModelConfig(raw []byte, ctx ConfigContext) (ModelConfig, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err == nil && keys != nil {
		if _, ok := keys["schema_version"]; !ok {
			legacy := false
			for _, key := range legacyOnlyKeys {
				if _, ok := keys[key]; ok {
					legacy = true
					break
				}
			}
			if !legacy {
				var config ModelConfig
				if err := json.Unmarshal(raw, &config); err != nil {
					return ModelConfig{}, fmt.Errorf("decode model config: %w", err)
				}
				return normalizeModelConfig(config), nil
			}
		}
	}
	return DecodeModelConfig(raw, ctx)
}

// LegacyModelConfig is the version 1 shape. Capabilities were a token list,
// and reasoning was a thinking mode plus flat fields; the effort list could
// carry "enabled" for a switch without levels and "disable" for a model that
// can be turned off. Besides reading old rows, the provider template import
// still merges discovered capabilities in this vocabulary; see
// MergeLegacyConfig.
type LegacyModelConfig struct {
	Description         *string  `json:"description,omitempty"`
	Dimensions          *int     `json:"dimensions,omitempty"`
	Compatibilities     []string `json:"compatibilities,omitempty"`
	ContextWindow       *int     `json:"context_window,omitempty"`
	ReasoningEfforts    []string `json:"reasoning_efforts,omitempty"`
	ThinkingMode        string   `json:"thinking_mode,omitempty"`
	CatalogAvailable    *bool    `json:"catalog_available,omitempty"`
	ReasoningDialect    string   `json:"reasoning_dialect,omitempty"`
	ReasoningOffSupport string   `json:"reasoning_off_support,omitempty"`
	ReasoningDefaultOn  *bool    `json:"reasoning_default_on,omitempty"`
	ThinkingBudgetMin   *int     `json:"thinking_budget_min,omitempty"`
	ThinkingBudgetMax   *int     `json:"thinking_budget_max,omitempty"`
	MaxOutputTokens     *int     `json:"max_output_tokens,omitempty"`
}

func migrateModelConfigV1(raw json.RawMessage, ctx ConfigContext) (json.RawMessage, error) {
	var v1 LegacyModelConfig
	if err := json.Unmarshal(raw, &v1); err != nil {
		return nil, err
	}
	return json.Marshal(v1.Upgrade(ctx))
}

// Upgrade maps a version 1 config onto version 2. Every model
// type keeps its description, limits, dimensions and catalog flag. Chat
// models also turn their capability tokens into modalities and tool calling,
// and their thinking fields into Reasoning; ModelConfig.ThinkingMode and
// ReasoningEfforts read Reasoning back as the version 1 runtime saw it.
func (v1 LegacyModelConfig) Upgrade(ctx ConfigContext) ModelConfig {
	out := ModelConfig{
		SchemaVersion:    ModelConfigSchemaVersion,
		Description:      v1.Description,
		ContextWindow:    v1.ContextWindow,
		MaxOutputTokens:  v1.MaxOutputTokens,
		Dimensions:       v1.Dimensions,
		CatalogAvailable: v1.CatalogAvailable,
	}
	if ctx.Type != "" && ctx.Type != ModelTypeChat {
		return out
	}
	has := func(token string) bool { return slices.Contains(v1.Compatibilities, token) }

	modalities := &ModelModalities{Input: []Modality{ModalityText}, Output: []Modality{ModalityText}}
	if has(CompatVision) {
		modalities.Input = append(modalities.Input, ModalityImage)
	}
	if has(CompatFileInput) {
		modalities.Input = append(modalities.Input, ModalityPDF)
	}
	if has(CompatImageOutput) {
		modalities.Output = append(modalities.Output, ModalityImage)
	}
	out.Modalities = modalities
	out.ToolCall = has(CompatToolCall)
	out.Reasoning = upgradeReasoningV1(v1, has(CompatReasoning), ctx)
	return out
}

func upgradeReasoningV1(v1 LegacyModelConfig, hasReasoning bool, ctx ConfigContext) *ModelReasoning {
	mode := reasoning.ResolveMode(v1.ThinkingMode, hasReasoning, ctx.ModelID)
	if !reasoning.Supported(mode) {
		return nil
	}
	r := &ModelReasoning{}
	if v1.ReasoningDefaultOn != nil {
		r.Default = &ReasoningDefault{Enabled: v1.ReasoningDefaultOn}
	}
	if mode == reasoning.ModeAlways {
		// Nothing reaches the model, so only a rejected off is worth keeping.
		if v1.ReasoningOffSupport == reasoning.OffSupportRejected {
			r.Off = reasoning.OffSupportRejected
		}
		return r
	}

	r.Dialect = v1.ReasoningDialect
	if mode == reasoning.ModeAdaptive {
		r.Dialect = reasoning.DialectAdaptive
	}
	r.Off = v1.ReasoningOffSupport

	// "enabled" and "disable" both declare an on/off switch. Off keeps its
	// version 1 meaning, the model's answer to an explicit disable, because the
	// Anthropic and OpenCode Go adaptors choose their wire from it.
	advertised := reasoning.NormalizeAdvertised(v1.ReasoningEfforts)
	for _, effort := range advertised {
		switch effort {
		case reasoning.EffortEnabled, reasoning.EffortDisable:
			r.Toggle = true
		default:
			r.Efforts = append(r.Efforts, effort)
		}
	}
	hasEnabled := slices.Contains(advertised, reasoning.EffortEnabled)
	hasBudget := v1.ThinkingBudgetMin != nil || v1.ThinkingBudgetMax != nil
	if len(r.Efforts) == 0 && !hasEnabled && !hasBudget {
		// Version 1 offered low, medium and high to a model that listed no tier,
		// whether it listed nothing or only the disable token, and sent medium
		// once reasoning was on. Only the enabled token made it a plain switch.
		// A budget is a control of its own: no shipped template paired one with
		// an empty tier list, and Legacy reads a budget-only row back without
		// tiers, so filling them here would add tiers on every round trip.
		r.Efforts = []string{reasoning.EffortLow, reasoning.EffortMedium, reasoning.EffortHigh}
	}
	if hasEnabled && !slices.Contains(advertised, reasoning.EffortDisable) && r.Off == "" &&
		ctx.ClientType != "" && !reasoning.OffByOmission(ctx.ClientType) {
		// A switch advertised without the disable token could only be turned off
		// where the protocol does so by leaving thinking out. Elsewhere version 1
		// offered no off at all, which version 2 states as rejected: a toggle with
		// an unstated Off would otherwise read as switchable off.
		r.Off = reasoning.OffSupportRejected
	}
	if v1.ThinkingBudgetMin != nil || v1.ThinkingBudgetMax != nil {
		r.Budget = &ReasoningBudget{Min: v1.ThinkingBudgetMin, Max: v1.ThinkingBudgetMax}
	}
	return r
}

// Legacy reads the config back in the version 1 vocabulary, as the version 1
// runtime would have seen it.
func (c ModelConfig) Legacy() LegacyModelConfig {
	out := LegacyModelConfig{
		Description:      c.Description,
		Dimensions:       c.Dimensions,
		ContextWindow:    c.ContextWindow,
		CatalogAvailable: c.CatalogAvailable,
		MaxOutputTokens:  c.MaxOutputTokens,
	}
	for _, token := range []string{CompatVision, CompatFileInput, CompatToolCall, CompatImageOutput, CompatReasoning} {
		if c.Supports(token) {
			out.Compatibilities = append(out.Compatibilities, token)
		}
	}
	// The mode reads "none" without Reasoning, as version 1 stored it: discovery
	// reports a mode for every model, so an empty one here would make every
	// non-reasoning model look changed on each managed refresh.
	out.ThinkingMode = c.ThinkingMode()
	if c.Reasoning != nil {
		out.ReasoningEfforts = c.ReasoningEfforts()
		out.ReasoningDialect = c.ReasoningDialect()
		out.ReasoningOffSupport = c.ReasoningOffSupport()
		out.ReasoningDefaultOn = c.ReasoningDefaultOn()
		out.ThinkingBudgetMin = c.ThinkingBudgetMin()
		out.ThinkingBudgetMax = c.ThinkingBudgetMax()
	}
	return out
}

// MergeLegacyConfig upgrades a config merged in the version 1 vocabulary and
// keeps what version 1 cannot express from existing: structured output,
// modalities other than text, image and PDF, the reasoning replay, and the
// default effort and budget.
func MergeLegacyConfig(existing ModelConfig, merged LegacyModelConfig, ctx ConfigContext) ModelConfig {
	out := merged.Upgrade(ctx)
	out.StructuredOutput = existing.StructuredOutput
	if existing.Modalities != nil && out.Modalities != nil {
		out.Modalities.Input = append(out.Modalities.Input, modalitiesBeyondV1(existing.Modalities.Input)...)
		out.Modalities.Output = append(out.Modalities.Output, modalitiesBeyondV1(existing.Modalities.Output)...)
	}
	if existing.Reasoning != nil && out.Reasoning != nil {
		out.Reasoning.Replay = existing.Reasoning.Replay
		if d := existing.Reasoning.Default; d != nil && (d.Effort != "" || d.Budget != nil) {
			if out.Reasoning.Default == nil {
				out.Reasoning.Default = &ReasoningDefault{}
			}
			out.Reasoning.Default.Effort = d.Effort
			out.Reasoning.Default.Budget = d.Budget
		}
	}
	return normalizeModelConfig(out)
}

func modalitiesBeyondV1(in []Modality) []Modality {
	var out []Modality
	for _, m := range in {
		switch m {
		case ModalityText, ModalityImage, ModalityPDF:
		default:
			out = append(out, m)
		}
	}
	return out
}
