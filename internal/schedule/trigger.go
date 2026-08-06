package schedule

import "context"

// TriggerPayload describes the parameters passed to the chat side when a schedule triggers.
type TriggerPayload struct {
	ID          string
	Name        string
	Description string
	Pattern     string
	MaxCalls    *int
	Command     string
	OwnerUserID string
	SessionID   string
	// ModelID / ACPModelID / ReasoningEffort are the schedule's per-run
	// overrides. Which runtime executes the run is decided by the session
	// itself (its runtime_type and metadata), never by the payload.
	ModelID         string
	ACPModelID      string
	ReasoningEffort string
}

// TriggerResult carries execution metadata back from the resolver.
type TriggerResult struct {
	Status     string
	Text       string
	UsageBytes []byte
	ModelID    string
}

// Triggerer triggers schedule execution for chat-related jobs.
type Triggerer interface {
	TriggerSchedule(ctx context.Context, botID string, payload TriggerPayload, token string) (TriggerResult, error)
}
