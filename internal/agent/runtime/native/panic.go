package native

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
)

// codeInternal is the catalog code of a failure nothing names. internal/apperror
// registers the same value.
const codeInternal = "internal"

// panicFailureEvent is the error event for a panic inside the run. The panic
// value is the event's private cause and never reaches a subscriber; the code
// is the catalog's catch-all.
func panicFailureEvent(r any) StreamEvent {
	return StreamEvent{Type: EventError, Code: codeInternal, Cause: fmt.Errorf("agent run panicked: %v", r)}
}

func (a *Agent) logPanic(ctx context.Context, where string, r any) {
	a.logger.ErrorContext(ctx, "agent run panicked",
		slog.String("where", where),
		slog.Any("panic", r),
		slog.String("stack", string(debug.Stack())),
	)
}

// containStreamPanic is deferred by the goroutine that runs a stream. A panic
// there ends that run with an internal failure instead of the process.
func (a *Agent) containStreamPanic(ctx context.Context, ch chan<- StreamEvent) {
	r := recover()
	if r == nil {
		return
	}
	a.logPanic(ctx, "stream", r)
	sendEvent(ctx, ch, panicFailureEvent(r))
}

// containEnginePanic is deferred by the step-loop goroutine, which the stream
// goroutine's recover cannot reach. The failure goes out as the engine's own
// failure event, so the stream ends the way any failed run does.
func (e *streamEngine) containEnginePanic() {
	r := recover()
	if r == nil {
		return
	}
	e.agent.logPanic(e.streamCtx, "step loop", r)
	e.endWithFailure(panicFailureEvent(r))
}
