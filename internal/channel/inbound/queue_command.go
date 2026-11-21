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
	// QueueCommandCodeFollowUpUnsupportedChannel answers /queue on a channel.
	// A follow-up is a run the server starts later, without the inbound
	// call's run handle a platform channel streams its reply from; only the
	// Web client, which watches the session, queues one, through its own API.
	QueueCommandCodeFollowUpUnsupportedChannel = string(apperror.CodeQueueFollowUpUnsupportedChannel)
)

// QueueCommandInput is a /steer a channel admits, with only facts derived by
// the channel boundary: the session is resolved from the current route, so
// callers cannot select a run. The steer joins the run the channel is
// already streaming, which has its own route; it records the team and the
// sender, who alone may edit it.
type QueueCommandInput struct {
	TeamID            string `json:"team_id"`
	BotID             string `json:"bot_id"`
	SessionID         string `json:"session_id"`
	InvocationID      string `json:"invocation_id"`
	UserID            string `json:"user_id,omitempty"`
	ChannelIdentityID string `json:"channel_identity_id,omitempty"`
	Text              string `json:"text"`
}

// QueueCommandHandler is the narrow live-queue port used by the channel
// /steer control. The embedded Server uses a local adapter; split Channel
// uses the authenticated server-runtime RPC client.
type QueueCommandHandler interface {
	EnqueueSteer(context.Context, QueueCommandInput) error
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
