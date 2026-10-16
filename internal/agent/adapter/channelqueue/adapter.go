// Package channelqueue adapts channel queue controls to the application
// service without exposing queue storage types to channels or RPC.
package channelqueue

import (
	"context"
	"strings"

	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/channel/inbound"
)

type Adapter struct{ service *application.Service }

func New(service *application.Service) *Adapter { return &Adapter{service: service} }

func (a *Adapter) EnqueueSteer(ctx context.Context, input inbound.QueueCommandInput) error {
	return a.enqueue(input, func(queued application.QueueInput) error {
		_, err := a.service.EnqueueSteer(ctx, queued)
		return err
	})
}

func (a *Adapter) EnqueueFollowUp(ctx context.Context, input inbound.QueueCommandInput) error {
	return a.enqueue(input, func(queued application.QueueInput) error {
		_, err := a.service.EnqueueFollowUp(ctx, queued)
		return err
	})
}

func (a *Adapter) enqueue(input inbound.QueueCommandInput, admit func(application.QueueInput) error) error {
	if a == nil || a.service == nil {
		return inbound.NewQueueCommandError(inbound.QueueCommandCodeUnavailable)
	}
	if strings.TrimSpace(input.BotID) == "" || strings.TrimSpace(input.SessionID) == "" ||
		strings.TrimSpace(input.InvocationID) == "" || strings.TrimSpace(input.Text) == "" {
		return inbound.NewQueueCommandError(inbound.QueueCommandCodeInvalid)
	}
	return mapAdmissionError(admit(application.QueueInput{
		TeamID:                  input.TeamID,
		BotID:                   input.BotID,
		SessionID:               input.SessionID,
		InvocationID:            input.InvocationID,
		UserID:                  input.UserID,
		SourceChannelIdentityID: input.ChannelIdentityID,
		Text:                    input.Text,
	}))
}

// admissionCodes renders queue admission refusals in the channel command
// vocabulary, which crosses the split-runtime RPC and is not the HTTP one.
var admissionCodes = map[application.QueueAdmissionFailure]string{
	application.QueueAdmissionSteerUnsupported:   inbound.QueueCommandCodeUnsupported,
	application.QueueAdmissionNoActiveRun:        inbound.QueueCommandCodeNoActiveRun,
	application.QueueAdmissionInvocationConflict: inbound.QueueCommandCodeConflict,
	application.QueueAdmissionOverloaded:         inbound.QueueCommandCodeOverloaded,
	application.QueueAdmissionCapacityExceeded:   inbound.QueueCommandCodeCapacity,
	application.QueueAdmissionInvalidReference:   inbound.QueueCommandCodeInvalid,
	application.QueueAdmissionUnavailable:        inbound.QueueCommandCodeUnavailable,
}

func mapAdmissionError(err error) error {
	code, ok := admissionCodes[application.QueueAdmissionFailureOf(err)]
	if !ok {
		return err
	}
	return inbound.NewQueueCommandError(code)
}

var _ inbound.QueueCommandHandler = (*Adapter)(nil)
