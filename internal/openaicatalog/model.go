// Package openaicatalog describes the account-specific OpenAI model catalog.
package openaicatalog

const ClientVersion = "1.0.0"

type Model struct {
	Slug                     string            `json:"slug"`
	DisplayName              string            `json:"display_name"`
	Visibility               string            `json:"visibility"`
	SupportedReasoningLevels []ReasoningEffort `json:"supported_reasoning_levels"`
	ContextWindow            *int              `json:"context_window"`
	MaxContextWindow         *int              `json:"max_context_window"`
	InputModalities          []string          `json:"input_modalities"`
}

type ReasoningEffort struct {
	Effort string `json:"effort"`
}
