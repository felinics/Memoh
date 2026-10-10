package inbound

import (
	"context"

	"github.com/felinics/memoh/internal/apperror"
)

// Queue command refusal codes for the local queue adapter, each the catalog
// code the refusal is answered with.
const (
	// QueueCommandCodeNoActiveRun means the route has no active run that can
	// accept a queue item. It is deliberately shared by an absent active
	// session and a session whose run ended between route lookup and admission.
	QueueCommandCodeNoActiveRun = string(apperror.CodeQueueNoActiveRun)
	QueueCommandCodeOverloaded  = string(apperror.CodeQueueAdmissionOverloaded)
	QueueCommandCodeUnavailable = string(apperror.CodeQueueAdmissionUnavailable)
	QueueCommandCodeConflict    = string(apperror.CodeQueueInvocationConflict)
	QueueCommandCodeInvalid     = string(apperror.CodeQueueRequestInvalid)
	QueueCommandCodeUnsupported = string(apperror.CodeQueueUnsupportedSession)
	QueueCommandCodeCapacity    = string(apperror.CodeQueueCapacityExceeded)
	// QueueCommandCodeFollowUpUnsupportedChannel means the channel cannot
	// receive the reply of a run that the server starts from the follow-up
	// queue: platform channels deliver replies from the inbound call's run
	// handle, which a queued run does not have.
	QueueCommandCodeFollowUpUnsupportedChannel = string(apperror.CodeQueueFollowUpUnsupportedChannel)
)

// QueueCommandInput contains only facts derived by the channel boundary. The
// session is resolved from the current route; callers cannot select a run or
// supply queue provenance. Team and sender identity are recorded with the
// item because a queued follow-up starts after this request is gone, and the
// admission that starts it needs the same identity an ordinary turn carries.
type QueueCommandInput struct {
	TeamID            string `json:"team_id"`
	BotID             string `json:"bot_id"`
	SessionID         string `json:"session_id"`
	InvocationID      string `json:"invocation_id"`
	UserID            string `json:"user_id,omitempty"`
	ChannelIdentityID string `json:"channel_identity_id,omitempty"`
	Text              string `json:"text"`
}

// QueueCommandHandler is the narrow live-queue port used by channel slash
// controls. The embedded Server uses a local adapter; split Channel uses the
// authenticated server-runtime RPC client.
type QueueCommandHandler interface {
	EnqueueSteer(context.Context, QueueCommandInput) error
	EnqueueFollowUp(context.Context, QueueCommandInput) error
}

// NewQueueCommandError is the public error a queue command is refused with.
// It carries no database or RPC diagnostic, and it crosses the split-runtime
// RPC as its catalog code.
func NewQueueCommandError(code string) error {
	return apperror.New(apperror.Code(code), nil)
}

// QueueCommandErrorCode is the code err is answered with, or "" when err
// carries none.
func QueueCommandErrorCode(err error) string {
	return string(apperror.CodeOf(err))
}
