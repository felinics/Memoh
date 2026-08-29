package application

import (
	"context"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
)

// subagentRunHandleKey carries the admitted run's handle through the context
// AdmitSubagentRun hands back to the tool layer. The tool layer never reads it
// — it treats the context as opaque — but the step committer and the runtime
// event observer this package installs on the spawn path need the run's
// identity (RunID, FencingToken) to write and publish under the right run.
type subagentRunHandleKey struct{}

// withSubagentRunHandle stores the admitted handle on the run context.
func withSubagentRunHandle(ctx context.Context, handle sessionruntime.RunHandle) context.Context {
	return context.WithValue(ctx, subagentRunHandleKey{}, handle)
}

// SubagentRunHandleFromContext returns the run handle AdmitSubagentRun stored
// on the context, if the context belongs to an admitted subagent run.
func SubagentRunHandleFromContext(ctx context.Context) (sessionruntime.RunHandle, bool) {
	if ctx == nil {
		return sessionruntime.RunHandle{}, false
	}
	handle, ok := ctx.Value(subagentRunHandleKey{}).(sessionruntime.RunHandle)
	return handle, ok
}
