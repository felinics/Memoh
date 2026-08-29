package application

import (
	"context"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	sessionqueue "github.com/felinics/memoh/internal/agent/runtime/session/queue"
)

// queueRuntimeStore is the application port for durable queue execution and
// recovery. HTTP mutation methods intentionally do not belong here: the agent
// loop can only consume already-admitted work under an owned run fence.
type queueRuntimeStore interface {
	NextQueueStepIndex(context.Context, string) (int, error)
	ListOwnerlessContinuations(context.Context, int32) ([]sessionqueue.OwnerlessContinuation, error)
	FollowUpByID(context.Context, sessionqueue.FollowUpItemID) (sessionqueue.FollowUpItem, error)
	GetContinuationRun(context.Context, string) (sessionqueue.ContinuationRun, error)
	AcquireContinuationRun(context.Context, string, string, string) (sessionruntime.RunHandle, bool, error)
	ClaimAssignedFollowUp(context.Context, string, sessionruntime.RunHandle) (sessionqueue.FollowUpItem, error)
	GetClaimedSteerForRun(context.Context, string) (sessionqueue.SteerItem, error)
	GetRecoverableRun(context.Context, string) (sessionqueue.RecoverableRun, error)
	AcquireQueuedRun(context.Context, string, int64, string, string) (sessionruntime.RunHandle, bool, error)
	ReclaimSteer(context.Context, string, sessionruntime.RunHandle) (sessionqueue.SteerItem, sessionqueue.SteerClaimRef, error)
}

var _ queueRuntimeStore = (*sessionqueue.PostgresStore)(nil)
