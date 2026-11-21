// Package channelqueue adapts the channel /steer control to the application
// service without exposing queue storage types to channels or RPC.
package channelqueue

import (
	"context"
	"strings"

	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/channel/inbound"
)

type Adapter struct{ service *application.Service }

func New(service *application.Service) *Adapter { return &Adapter{service: service} }

// EnqueueSteer admits a channel /steer. The steer joins the run the channel
// is already streaming, so its command needs only the team, the sender, and
// the text.
func (a *Adapter) EnqueueSteer(ctx context.Context, input inbound.QueueCommandInput) error {
	if a == nil || a.service == nil {
		return inbound.NewQueueCommandError(inbound.QueueCommandCodeUnavailable)
	}
	text := strings.TrimSpace(input.Text)
	_, err := a.service.EnqueueSteer(ctx, input.InvocationID, turn.StartTurnCommand{
		SchemaVersion:           1,
		TeamID:                  input.TeamID,
		Mode:                    turn.ModeChat,
		BotID:                   input.BotID,
		ChatID:                  input.BotID,
		ThreadID:                input.SessionID,
		UserID:                  input.UserID,
		SourceChannelIdentityID: input.ChannelIdentityID,
		Query:                   text,
		UserVisibleText:         text,
	})
	return mapAdmissionError(err)
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
