package compaction

// TriggerModel is the resolved summarizer identity handed to the engine:
// selection policy and credential resolution belong to the orchestration
// surfaces, the engine only receives this completed contract.
type TriggerModel struct {
	Slug                  string // runtime model slug sent to the provider
	RecordID              string // models.id row UUID recorded as artifact/log provenance
	ClientType            string
	APIKey                string //nolint:gosec // runtime credential, not a hardcoded secret
	CodexAccountID        string
	BaseURL               string
	ChatCompletionsCompat string
	PromptCacheTTL        string
	WindowTokens          int // summarizer model's declared context window
}

// NewTriggerConfig derives both window budgets from a resolved summarizer:
// candidate selection is capped at 85% of the window so the prompt keeps
// headroom for the system prompt and provider framing on top of the summary
// output reserve.
func NewTriggerConfig(model TriggerModel) TriggerConfig {
	cfg := TriggerConfig{
		ModelID:               model.Slug,
		ModelRecordID:         model.RecordID,
		ClientType:            model.ClientType,
		APIKey:                model.APIKey,
		CodexAccountID:        model.CodexAccountID,
		BaseURL:               model.BaseURL,
		ChatCompletionsCompat: model.ChatCompletionsCompat,
		PromptCacheTTL:        model.PromptCacheTTL,
		SummaryWindowTokens:   model.WindowTokens,
	}
	if model.WindowTokens > 0 {
		cfg.MaxCompactTokens = model.WindowTokens * 85 / 100
	}
	return cfg
}
