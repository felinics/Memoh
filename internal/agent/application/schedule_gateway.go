package application

import (
	"context"
	"errors"

	"github.com/felinics/memoh/internal/schedule"
)

// ScheduleGateway adapts schedule trigger calls to the chat Service.
type ScheduleGateway struct {
	service *Service
}

// NewScheduleGateway creates a ScheduleGateway backed by the given Service.
func NewScheduleGateway(service *Service) *ScheduleGateway {
	return &ScheduleGateway{service: service}
}

// TriggerSchedule delegates a schedule trigger to the chat Service.
func (g *ScheduleGateway) TriggerSchedule(ctx context.Context, botID string, payload schedule.TriggerPayload, token string) (schedule.TriggerResult, error) {
	if g == nil || g.service == nil {
		return schedule.TriggerResult{}, errors.New("agent application service not configured")
	}
	return g.service.TriggerSchedule(ctx, botID, payload, token)
}
