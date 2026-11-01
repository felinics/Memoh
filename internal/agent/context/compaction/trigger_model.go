package compaction

// TriggerModel is the resolved summarizer identity handed to the engine:
// selection policy and credential resolution belong to the orchestration
// surfaces, the engine only receives this completed contract.
type TriggerModel struct {
	Slug                  string // runtime model slug sent to the provider
	RecordID              string // models.id row UUID recorded as the log's provenance
	ClientType            string
	APIKey                string //nolint:gosec // runtime credential, not a hardcoded secret
	CodexAccountID        string
	BaseURL               string
	ChatCompletionsCompat string
	WindowTokens          int // summarizer model's declared context window
	MaxOutputTokens       int // summarizer model's declared output cap, zero when unknown
}

// NewTriggerConfig carries a resolved summarizer into the engine.
func NewTriggerConfig(model TriggerModel) TriggerConfig {
	return TriggerConfig{
		ModelID:               model.Slug,
		ModelRecordID:         model.RecordID,
		ClientType:            model.ClientType,
		APIKey:                model.APIKey,
		CodexAccountID:        model.CodexAccountID,
		BaseURL:               model.BaseURL,
		ChatCompletionsCompat: model.ChatCompletionsCompat,
		SummaryWindowTokens:   model.WindowTokens,
		MaxOutputTokens:       model.MaxOutputTokens,
	}
}
