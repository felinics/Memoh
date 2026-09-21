package native

import (
	"github.com/felinics/twilight/sdk"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/felinics/memoh/internal/telemetry"
)

// wrapToolTracing puts a span around every tool execution.
//
// A turn that took ninety seconds is a question, not an answer. Tool calls are
// usually where the time went — a command in the workspace container, a page
// load in the headed browser, an MCP round trip — and which one it was is
// invisible without a span per call.
//
// This wraps in assembleTools rather than at the three places that decorate
// its result, so a fourth caller cannot arrive without it. The decorator shape
// matches wrapToolUIOutput and WrapToolOutputLimits, which sit in the same
// chain.
//
// The arguments are not recorded and must not be: a tool call carries file
// contents, shell commands, and whatever the user asked for, which is the
// material docs/logging.md keeps out of log records. The name of the tool and
// whether it failed is what a trace needs; the rest is in the conversation.
func wrapToolTracing(sdkTools []sdk.Tool) []sdk.Tool {
	if len(sdkTools) == 0 {
		return sdkTools
	}
	wrapped := make([]sdk.Tool, len(sdkTools))
	copy(wrapped, sdkTools)
	for i := range wrapped {
		execute := wrapped[i].Execute
		if execute == nil {
			continue
		}
		name := wrapped[i].Name
		wrapped[i].Execute = func(execCtx *sdk.ToolExecContext, input any) (any, error) {
			if execCtx == nil {
				return execute(execCtx, input)
			}
			ctx, span := telemetry.Tracer().Start(execCtx.Context, "agent.tool "+name,
				trace.WithSpanKind(trace.SpanKindInternal),
				trace.WithAttributes(attribute.String("agent.tool.name", name)),
			)
			defer span.End()

			// The tool reads its context from here, so the span has to be put
			// back for anything the tool does — a workspace RPC, a database
			// read — to hang beneath it rather than float at the trace root.
			scoped := *execCtx
			scoped.Context = ctx

			output, err := execute(&scoped, input)
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, "")
			}
			return output, err
		}
	}
	return wrapped
}
