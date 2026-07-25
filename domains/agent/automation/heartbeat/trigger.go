package heartbeat

import "context"

type TriggerPayload struct {
	BotID           string
	Interval        int
	OwnerUserID     string
	SessionID       string
	LastHeartbeatAt string // ISO 8601; empty on first heartbeat
}

type TriggerResult struct {
	Status     string
	Text       string
	Usage      any
	UsageBytes []byte
	ModelID    string
	SessionID  string
}

type Triggerer interface {
	TriggerHeartbeat(ctx context.Context, botID string, payload TriggerPayload, token string) (TriggerResult, error)
}
