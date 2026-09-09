package models

const (
	// DefaultOutputReserveTokens is the completion allowance reserved for a
	// turn when the provider's own cap is not mirrored exactly.
	DefaultOutputReserveTokens = 8192
	// DefaultReasoningOutputReserveTokens is the allowance when thinking is
	// active; reasoning output shares the completion cap on every provider.
	DefaultReasoningOutputReserveTokens = 32000

	anthropicDefaultMaxTokens          = 4096
	anthropicDefaultReasoningMaxTokens = 32000
	anthropicMinThinkingBudgetTokens   = 1024
)

const (
	GenerationLimitsProviderDefault = "provider_default"
	GenerationLimitsEstimated       = "estimated"
	GenerationLimitsProviderIgnores = "provider_ignores"
	GenerationLimitsWindowClamped   = "window_clamped"
)

// GenerationLimits is the single authority for one turn's output allowance:
// the context budget plan reserves MaxOutputTokens and, when Requested, the
// provider request carries the same value as max_tokens. Unrequested limits
// are Memoh's reserve only; the provider keeps its own default because the
// model's real cap is unknown and an explicit value could be rejected.
type GenerationLimits struct {
	MaxOutputTokens int
	Requested       bool
	Resolution      string
}

// ResolveGenerationLimits derives the output allowance from the client type,
// the resolved thinking decision, and the configured context window.
// Anthropic mirrors the SDK's own defaults, so the reserved and requested
// values cannot diverge; every other client is estimated without a request.
func ResolveGenerationLimits(clientType ClientType, reasoning *ReasoningConfig, contextWindow int) GenerationLimits {
	if clientType == ClientTypeAnthropicMessages {
		return anthropicGenerationLimits(reasoning, contextWindow)
	}
	limits := GenerationLimits{MaxOutputTokens: DefaultOutputReserveTokens, Resolution: GenerationLimitsEstimated}
	if reasoning != nil && reasoning.Active {
		limits.MaxOutputTokens = DefaultReasoningOutputReserveTokens
	}
	if contextWindow > 0 && limits.MaxOutputTokens > contextWindow/4 {
		limits.MaxOutputTokens = contextWindow / 4
		limits.Resolution = GenerationLimitsWindowClamped
	}
	if !EnforcesMaxOutputTokens(clientType) {
		limits.Resolution = GenerationLimitsProviderIgnores
	}
	return limits
}

// anthropicGenerationLimits reproduces the adapter's max_tokens resolution:
// the answer allowance, the reasoning-aware default for adaptive thinking, or
// the answer allowance plus the legacy thinking budget. Thinking may use at
// most half of a configured window, down to the minimum budget the API
// accepts.
func anthropicGenerationLimits(reasoning *ReasoningConfig, contextWindow int) GenerationLimits {
	limits := GenerationLimits{
		MaxOutputTokens: anthropicDefaultMaxTokens,
		Requested:       true,
		Resolution:      GenerationLimitsProviderDefault,
	}
	switch {
	case reasoning == nil || !reasoning.Active:
		return limits
	case reasoning.Adaptive:
		limits.MaxOutputTokens = anthropicDefaultReasoningMaxTokens
		if contextWindow > 0 && limits.MaxOutputTokens > contextWindow/2 {
			limits.MaxOutputTokens = contextWindow / 2
			limits.Resolution = GenerationLimitsWindowClamped
		}
	default:
		budget := AnthropicThinkingBudget(reasoning.Effort, contextWindow)
		limits.MaxOutputTokens = anthropicDefaultMaxTokens + budget
		if budget < legacyAnthropicBudgetFor(reasoning.Effort) {
			limits.Resolution = GenerationLimitsWindowClamped
		}
	}
	return limits
}

// AnthropicThinkingBudget is the legacy budget_tokens sent for an effort,
// fitted so the answer allowance plus the budget stays within half of the
// configured window but never below the minimum the API accepts; the model
// construction and the budget plan share it so budget_tokens always stays
// below the requested max_tokens.
func AnthropicThinkingBudget(effort string, contextWindow int) int {
	budget := legacyAnthropicBudgetFor(effort)
	if contextWindow > 0 {
		budget = min(budget, max(anthropicMinThinkingBudgetTokens, contextWindow/2-anthropicDefaultMaxTokens))
	}
	return budget
}
