package application

import (
	"context"
	"errors"

	"github.com/memohai/memoh/domains/agent/automation/heartbeat"
)

// HeartbeatGateway adapts heartbeat trigger calls to the chat Service.
type HeartbeatGateway struct {
	service *Service
}

// NewHeartbeatGateway creates a HeartbeatGateway backed by the given Service.
func NewHeartbeatGateway(service *Service) *HeartbeatGateway {
	return &HeartbeatGateway{service: service}
}

// TriggerHeartbeat delegates a heartbeat trigger to the chat Service.
func (g *HeartbeatGateway) TriggerHeartbeat(ctx context.Context, botID string, payload heartbeat.TriggerPayload, token string) (heartbeat.TriggerResult, error) {
	if g == nil || g.service == nil {
		return heartbeat.TriggerResult{}, errors.New("agent application service not configured")
	}
	return g.service.TriggerHeartbeat(ctx, botID, payload, token)
}
