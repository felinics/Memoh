package application

import (
	"context"

	"github.com/felinics/memoh/internal/schedule"
)

// Runner defines conversation execution behavior for sync, stream, and scheduled flows.
type Runner interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	StreamChat(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error)
	TriggerSchedule(ctx context.Context, botID string, payload schedule.TriggerPayload, token string) (schedule.TriggerResult, error)
}
