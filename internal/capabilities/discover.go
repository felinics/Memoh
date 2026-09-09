package capabilities

import (
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/reasoning"
)

// litellmEntry is the subset of a LiteLLM registry record we consume. All
// capability fields are pointers so we can distinguish "registry is silent"
// (nil) from an explicit false.
type litellmEntry struct {
	Mode string `json:"mode"`

	SupportsReasoning        *bool `json:"supports_reasoning"`
	SupportsAdaptiveThinking *bool `json:"supports_adaptive_thinking"`

	SupportsNoneReasoningEffort    *bool `json:"supports_none_reasoning_effort"`
	SupportsMinimalReasoningEffort *bool `json:"supports_minimal_reasoning_effort"`
	SupportsLowReasoningEffort     *bool `json:"supports_low_reasoning_effort"`
	SupportsXHighReasoningEffort   *bool `json:"supports_xhigh_reasoning_effort"`
	SupportsMaxReasoningEffort     *bool `json:"supports_max_reasoning_effort"`

	SupportsVision          *bool `json:"supports_vision"`
	SupportsFunctionCalling *bool `json:"supports_function_calling"`
	SupportsPDFInput        *bool `json:"supports_pdf_input"`

	MaxInputTokens *int `json:"max_input_tokens"`
}

// Capabilities is the fill payload derived from a registry entry. A zero/empty
// field means "registry did not provide this"; callers must only fill missing
// upstream values and never override explicit ones.
type Capabilities struct {
	// ThinkingMode is "", toggle, adaptive, or none. "" = unknown.
	ThinkingMode string
	// EffortLevels is the ordered effort tier list, or nil if unknown.
	EffortLevels []string
	// Vision / ToolCall / FileInput are nil when the registry is silent.
	Vision   *bool
	ToolCall *bool
	// FileInput mirrors supports_pdf_input: the provider API accepts documents
	// (PDF) as native input parts.
	FileInput *bool
	// ContextWindow is nil when unknown.
	ContextWindow *int
}

// effortOrder is the canonical low→high ordering used to render effort lists.
// "off" leads because it is the weakest thing a user can ask for, even though it
// is not a tier — which is why it is prepended here rather than living in the
// reasoning package's tier ordering.
var effortOrder = append(
	[]string{reasoning.EffortDisable},
	reasoning.OrderedEfforts()...,
)

func boolVal(p *bool) bool { return p != nil && *p }

// derive maps a registry entry to capabilities.
func derive(e litellmEntry) Capabilities {
	caps := Capabilities{
		Vision:        e.SupportsVision,
		ToolCall:      e.SupportsFunctionCalling,
		FileInput:     e.SupportsPDFInput,
		ContextWindow: e.MaxInputTokens,
	}

	anyEffortFlag := boolVal(e.SupportsNoneReasoningEffort) ||
		boolVal(e.SupportsMinimalReasoningEffort) ||
		boolVal(e.SupportsLowReasoningEffort) ||
		boolVal(e.SupportsXHighReasoningEffort) ||
		boolVal(e.SupportsMaxReasoningEffort)

	reasoningSupported := boolVal(e.SupportsReasoning) || boolVal(e.SupportsAdaptiveThinking) || anyEffortFlag

	switch {
	case reasoningSupported:
		if boolVal(e.SupportsAdaptiveThinking) {
			caps.ThinkingMode = models.ThinkingModeAdaptive
		} else {
			caps.ThinkingMode = models.ThinkingModeToggle
		}
		caps.EffortLevels = deriveEffortLevels(e)
	case e.SupportsReasoning != nil && !*e.SupportsReasoning:
		// Registry explicitly says no reasoning.
		caps.ThinkingMode = models.ThinkingModeNone
	default:
		// Registry silent on reasoning → leave unknown.
	}

	return caps
}

// deriveEffortLevels builds the effort tier list. medium/high form the implicit
// base for any reasoning model (the registry has no per-tier flag for them). low
// is part of that base unless the registry explicitly disables it
// (supports_low_reasoning_effort: false, e.g. gpt-5.5-pro). none/minimal/xhigh/max
// are added only when their explicit flags are set.
func deriveEffortLevels(e litellmEntry) []string {
	present := map[string]bool{
		models.ReasoningEffortMedium: true,
		models.ReasoningEffortHigh:   true,
	}
	if e.SupportsLowReasoningEffort == nil || *e.SupportsLowReasoningEffort {
		present[models.ReasoningEffortLow] = true
	}
	// The registry flag is named after OpenAI's wire value, but what it tells us is
	// that the model can be turned off, so it lands on our neutral token.
	if boolVal(e.SupportsNoneReasoningEffort) {
		present[models.ReasoningEffortDisable] = true
	}
	if boolVal(e.SupportsMinimalReasoningEffort) {
		present[models.ReasoningEffortMinimal] = true
	}
	if boolVal(e.SupportsXHighReasoningEffort) {
		present[models.ReasoningEffortXHigh] = true
	}
	if boolVal(e.SupportsMaxReasoningEffort) {
		present[models.ReasoningEffortMax] = true
	}

	out := make([]string, 0, len(present))
	for _, tier := range effortOrder {
		if present[tier] {
			out = append(out, tier)
		}
	}
	return out
}
