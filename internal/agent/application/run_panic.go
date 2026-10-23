package application

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/felinics/memoh/internal/apperror"
)

// panickedRunCause is how a panic inside a run is reported to the failure
// path: the catalog's catch-all, since a panic names nothing a user could act
// on. The panic value stays behind it as the private cause.
func panickedRunCause(r any) error {
	return apperror.Wrap(apperror.CodeInternal, fmt.Errorf("agent run panicked: %v", r), nil)
}

// settlePanickedRun handles the value recovered from a goroutine that hosts a
// run. The panic ends that run rather than the process, which would take every
// other session with it. It is logged with its stack, and settle hands the
// failure to the path the run already uses to end. The caller recovers: a
// recover only takes effect when the deferred function itself calls it.
func settlePanickedRun(ctx context.Context, logger *slog.Logger, where string, ids []any, r any, settle func(error)) {
	if logger != nil {
		args := append([]any{
			slog.String("where", where),
			slog.Any("panic", r),
			slog.String("stack", string(debug.Stack())),
		}, ids...)
		logger.ErrorContext(ctx, "agent run panicked", args...)
	}
	if settle != nil {
		settle(panickedRunCause(r))
	}
}

// runIDs are the identifiers a recovered panic is logged under.
func runIDs(botID, sessionID, runID string) []any {
	return []any{
		slog.String("bot_id", botID),
		slog.String("session_id", sessionID),
		slog.String("run_id", runID),
	}
}

// containedRun runs fn and returns the failure a panic in it becomes.
func (s *Service) containedRun(ctx context.Context, where string, ids []any, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			settlePanickedRun(ctx, s.logger, where, ids, r, func(cause error) { err = cause })
		}
	}()
	return fn()
}
