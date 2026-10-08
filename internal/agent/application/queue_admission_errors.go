package application

import (
	"errors"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
)

// QueueAdmissionFailure names why queue admission refused an input. It is the
// transport-neutral half of the queue error translation: every ingress
// classifies with QueueAdmissionFailureOf and renders the failure in its own
// published vocabulary, which differs between HTTP and channels and does not
// change here.
type QueueAdmissionFailure int

const (
	// QueueAdmissionNotRefused is the zero value: the error is nil or is not
	// an admission refusal, and the caller handles it as an ordinary error.
	QueueAdmissionNotRefused QueueAdmissionFailure = iota
	QueueAdmissionSteerUnsupported
	QueueAdmissionNoActiveRun
	QueueAdmissionInvocationConflict
	QueueAdmissionOverloaded
	QueueAdmissionCapacityExceeded
	QueueAdmissionInvalidReference
	// QueueAdmissionUnavailable is a server wiring fault at the ingress, so
	// senders see it as unavailable rather than as a bad request.
	QueueAdmissionUnavailable
)

// QueueAdmissionFailureOf classifies an error returned by EnqueueSteer or
// EnqueueFollowUp.
func QueueAdmissionFailureOf(err error) QueueAdmissionFailure {
	switch {
	case err == nil:
		return QueueAdmissionNotRefused
	case errors.Is(err, sessionruntime.ErrQueueSteerUnsupported):
		return QueueAdmissionSteerUnsupported
	case errors.Is(err, sessionruntime.ErrQueueNoActiveRun):
		return QueueAdmissionNoActiveRun
	case errors.Is(err, sessionruntime.ErrQueueInvocationConflict):
		return QueueAdmissionInvocationConflict
	case errors.Is(err, sessionruntime.ErrQueueAdmissionOverloaded):
		return QueueAdmissionOverloaded
	case errors.Is(err, sessionruntime.ErrQueueCapacityExceeded):
		return QueueAdmissionCapacityExceeded
	case errors.Is(err, sessionruntime.ErrQueueInvalidReference):
		return QueueAdmissionInvalidReference
	case errors.Is(err, ErrQueueInputIncomplete):
		return QueueAdmissionUnavailable
	default:
		return QueueAdmissionNotRefused
	}
}
