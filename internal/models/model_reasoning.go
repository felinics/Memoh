package models

import (
	"slices"

	"github.com/felinics/memoh/internal/reasoning"
)

// This file projects the stored ModelReasoning onto the inputs the reasoning
// package decides with: a thinking mode, an advertised effort list that may
// carry the enabled and disable tokens, a dialect, and an off-support answer.
// The projection is the inverse of the version 1 migration in
// config_migration.go, so a migrated row resolves exactly as it did before.

// Controllable reports whether a request can influence reasoning at all.
func (r *ModelReasoning) Controllable() bool {
	return r != nil && (r.Toggle || len(r.Efforts) > 0 || r.Budget != nil)
}

// CanTurnOff reports whether a request can stop the model reasoning. A stated
// Off decides; when it is not stated, a toggle is taken to switch it off.
func (r *ModelReasoning) CanTurnOff() bool {
	if r == nil {
		return false
	}
	switch r.Off {
	case reasoning.OffSupportAccepted, reasoning.OffSupportLowEffortOnly:
		return true
	case reasoning.OffSupportRejected:
		return false
	default:
		return r.Toggle
	}
}

// ThinkingMode returns the model's thinking mode: none without reasoning,
// adaptive for the adaptive dialect, always when no request can influence
// reasoning, and toggle otherwise.
func (c ModelConfig) ThinkingMode() string {
	r := c.Reasoning
	switch {
	case r == nil:
		return reasoning.ModeNone
	case r.Dialect == reasoning.DialectAdaptive:
		return reasoning.ModeAdaptive
	case r.Controllable() || r.CanTurnOff():
		return reasoning.ModeToggle
	default:
		return reasoning.ModeAlways
	}
}

// ReasoningEfforts returns the advertised effort list: the effort levels, then
// for a toggle the enabled token when there is no level or budget to pick, and
// the disable token unless the model rejects being turned off. A declared Off
// without a toggle adds nothing: the reasoning package reads it directly.
func (c ModelConfig) ReasoningEfforts() []string {
	r := c.Reasoning
	if r == nil {
		return nil
	}
	out := slices.Clone(r.Efforts)
	if r.Toggle && len(r.Efforts) == 0 && r.Budget == nil {
		out = append(out, reasoning.EffortEnabled)
	}
	if r.Toggle && r.Off != reasoning.OffSupportRejected {
		out = append(out, reasoning.EffortDisable)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ReasoningDialect returns the wire dialect for the tier, budget and toggle
// shapes. Adaptive is a thinking mode, not a dialect, so it reads as empty.
func (c ModelConfig) ReasoningDialect() string {
	if c.Reasoning == nil || c.Reasoning.Dialect == reasoning.DialectAdaptive {
		return ""
	}
	return c.Reasoning.Dialect
}

// ReasoningOffSupport returns how the model answers an explicit disable.
func (c ModelConfig) ReasoningOffSupport() string {
	if c.Reasoning == nil {
		return ""
	}
	return c.Reasoning.Off
}

// ReasoningDefaultOn reports whether the model reasons when a request sets no
// control; nil means not stated.
func (c ModelConfig) ReasoningDefaultOn() *bool {
	if c.Reasoning == nil || c.Reasoning.Default == nil {
		return nil
	}
	return c.Reasoning.Default.Enabled
}

// ThinkingBudgetMin returns the lower bound of the reasoning token budget.
func (c ModelConfig) ThinkingBudgetMin() *int {
	if c.Reasoning == nil || c.Reasoning.Budget == nil {
		return nil
	}
	return c.Reasoning.Budget.Min
}

// ThinkingBudgetMax returns the upper bound of the reasoning token budget.
func (c ModelConfig) ThinkingBudgetMax() *int {
	if c.Reasoning == nil || c.Reasoning.Budget == nil {
		return nil
	}
	return c.Reasoning.Budget.Max
}

// ReasoningReplay returns the model's declared replay, or nil.
func (c ModelConfig) ReasoningReplay() *ReasoningReplay {
	if c.Reasoning == nil {
		return nil
	}
	return c.Reasoning.Replay
}

// inferUndeclaredDialect gives a reasoning model written without a dialect the
// adaptive one when its id reads as Claude 4.6 or later, the inference version
// 1 made for an undeclared thinking mode. Those generations reject the older
// budget wire, so a hand-added Claude model must not fall back to it.
// StoredForm returns the config as Create and Update store it: normalized, and
// with the dialect the model's id implies when none is declared. A caller that
// compares a stored row with a config it is about to write must compare the
// stored form, or the write-time changes read as a difference on every pass.
func (c ModelConfig) StoredForm(modelID string) ModelConfig {
	return inferUndeclaredDialect(normalizeModelConfig(c), modelID)
}

func inferUndeclaredDialect(c ModelConfig, modelID string) ModelConfig {
	if c.Reasoning == nil || c.Reasoning.Dialect != "" {
		return c
	}
	if reasoning.ResolveMode("", true, modelID) != reasoning.ModeAdaptive {
		return c
	}
	r := *c.Reasoning
	r.Dialect = reasoning.DialectAdaptive
	c.Reasoning = &r
	return c
}

// Supports reports whether the model has a capability: vision and file-input
// are image and PDF input, image-output is image output, tool-call is tool
// calling, and reasoning is any reasoning at all.
func (c ModelConfig) Supports(capability string) bool {
	switch capability {
	case CompatVision:
		return c.Accepts(ModalityImage)
	case CompatFileInput:
		return c.Accepts(ModalityPDF)
	case CompatImageOutput:
		return c.Produces(ModalityImage)
	case CompatToolCall:
		return c.ToolCall
	case CompatReasoning:
		return c.Reasoning != nil
	default:
		return false
	}
}
