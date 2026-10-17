package bridge

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AuxiliaryLoadTimeout bounds each best-effort workspace read made while
// preparing a chat turn: target resolution, hooks, skills and context files.
// It only catches a workspace that has stopped responding, so it sits well
// above a slow but healthy bridge. Explicit failures degrade without waiting
// for it, and once one read marks the workspace unavailable the remaining
// reads of the turn are skipped instead of each waiting again.
const AuxiliaryLoadTimeout = 15 * time.Second

// AuxiliaryContext bounds one auxiliary workspace read by AuxiliaryLoadTimeout.
func AuxiliaryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, AuxiliaryLoadTimeout)
}

// IsDeadlineExceeded reports whether a bridge call ran out of time: either
// the caller's deadline expired or the workspace provider reported its own
// timeout. Both arrive as a DeadlineExceeded status, which is neither
// ErrUnavailable nor context.DeadlineExceeded on the error chain.
func IsDeadlineExceeded(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded
}

// IsUnreachable reports whether err shows the workspace itself cannot serve
// requests right now, as opposed to a failure of one request.
func IsUnreachable(err error) bool {
	return errors.Is(err, ErrUnavailable) || IsDeadlineExceeded(err)
}

type workspaceAvailabilityKey struct{}

type workspaceAvailability struct {
	unavailable atomic.Bool
}

// WithWorkspaceAvailability scopes workspace availability to one chat turn.
// It is idempotent: a context that already carries the state keeps it, so
// every stage of the turn observes the same verdict.
func WithWorkspaceAvailability(ctx context.Context) context.Context {
	if availabilityFrom(ctx) != nil {
		return ctx
	}
	return WithNewWorkspaceAvailability(ctx)
}

// WithNewWorkspaceAvailability starts availability state of its own even
// inside a scoped turn. Work that outlives the turn it was started from, such
// as a background task, uses it so the two never share a verdict.
func WithNewWorkspaceAvailability(ctx context.Context) context.Context {
	return context.WithValue(ctx, workspaceAvailabilityKey{}, &workspaceAvailability{})
}

// MarkWorkspaceUnavailable records that the turn's workspace cannot be used.
// It is a no-op outside a turn scoped by WithWorkspaceAvailability.
func MarkWorkspaceUnavailable(ctx context.Context) {
	if state := availabilityFrom(ctx); state != nil {
		state.unavailable.Store(true)
	}
}

// MarkWorkspaceUnavailableOn marks the turn's workspace unavailable when err
// shows it is unreachable, and reports whether it did.
func MarkWorkspaceUnavailableOn(ctx context.Context, err error) bool {
	if err == nil || !IsUnreachable(err) || ctx.Err() != nil {
		return false
	}
	MarkWorkspaceUnavailable(ctx)
	return true
}

// WorkspaceUnavailableFromContext reports whether an earlier stage of the
// turn found the workspace unavailable.
func WorkspaceUnavailableFromContext(ctx context.Context) bool {
	state := availabilityFrom(ctx)
	return state != nil && state.unavailable.Load()
}

func availabilityFrom(ctx context.Context) *workspaceAvailability {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(workspaceAvailabilityKey{}).(*workspaceAvailability)
	return state
}
