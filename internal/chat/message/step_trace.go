package message

import "encoding/json"

const (
	// StepTraceMetadataKey stores the server-observed request trace on the
	// assistant row that carries the corresponding model step.
	StepTraceMetadataKey = "step_trace"
	StepTraceVersion     = 1
)

// StepTraceMetadata is the versioned persistence envelope for one model
// request: wall-clock boundaries in Unix milliseconds plus the provider usage
// it reported. It lives in metadata so it is never replayed to a provider.
type StepTraceMetadata struct {
	Version        int             `json:"version"`
	StepIndex      int             `json:"step_index"`
	StartedAtMS    int64           `json:"started_at_ms"`
	FirstTokenAtMS int64           `json:"first_token_at_ms,omitempty"`
	EndedAtMS      int64           `json:"ended_at_ms"`
	FinishReason   string          `json:"finish_reason,omitempty"`
	Usage          *StepTraceUsage `json:"usage,omitempty"`
}

// StepTraceUsage mirrors the provider usage of one request. InputTokens
// counts every prompt token the provider billed, cache reads and writes
// included, whatever the provider's own convention; CachedInputTokens is the
// cache-read share of it.
type StepTraceUsage struct {
	InputTokens       int `json:"input_tokens,omitempty"`
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
	CacheWriteTokens  int `json:"cache_write_tokens,omitempty"`
	OutputTokens      int `json:"output_tokens,omitempty"`
	ReasoningTokens   int `json:"reasoning_tokens,omitempty"`
}

// StepTraceFromMetadata decodes the versioned envelope from a message metadata
// map, tolerating both typed values and JSONB-loaded maps.
func StepTraceFromMetadata(metadata map[string]any) *StepTraceMetadata {
	if len(metadata) == 0 {
		return nil
	}
	value, ok := metadata[StepTraceMetadataKey]
	if !ok || value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var trace StepTraceMetadata
	if err := json.Unmarshal(data, &trace); err != nil || trace.Version != StepTraceVersion {
		return nil
	}
	return &trace
}
