package native

import (
	"context"
	"errors"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"

	"github.com/felinics/memoh/internal/agent/toolexec"
)

// callEndings are the ways a model or tool call's context ends. Only the
// first two are the caller cancelling the call; the idle timeout and a lost
// run are this process ending it for a reason, and the call failed.
func callEndings() []struct {
	name       string
	cause      error
	wantFailed bool
} {
	return []struct {
		name       string
		cause      error
		wantFailed bool
	}{
		{name: "stopped", cause: context.Canceled},
		{name: "caller deadline", cause: context.DeadlineExceeded},
		{name: "idle timeout", cause: errors.New("agent response idle timeout"), wantFailed: true},
		{name: "run lost", cause: errors.New("run ownership lost"), wantFailed: true},
	}
}

func endedContext(cause error) context.Context {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	return ctx
}

func TestModelSpanCountsOnlyTheCallersCancellationAsAborted(t *testing.T) {
	for _, tt := range callEndings() {
		t.Run(tt.name, func(t *testing.T) {
			recorder := recordToolSpans(t)
			ctx := endedContext(tt.cause)
			_, span := otel.Tracer("test").Start(ctx, "agent.model.stream")

			endCallSpan(ctx, span, ctx.Err())

			ended := findSpan(t, recorder, "agent.model.stream")
			outcome, _ := spanAttr(ended, "agent.model.outcome")
			wantOutcome, wantCode := "aborted", codes.Unset
			if tt.wantFailed {
				wantOutcome, wantCode = "errored", codes.Error
			}
			if outcome.AsString() != wantOutcome || ended.Status().Code != wantCode {
				t.Fatalf("outcome = %q, status = %v; want %q, %v", outcome.AsString(), ended.Status().Code, wantOutcome, wantCode)
			}
			if tt.wantFailed && (len(ended.Events()) == 0 || ended.Events()[0].Name != "exception") {
				t.Fatal("a failed call did not record why it ended")
			}
		})
	}
}

func TestToolSpanCountsOnlyTheCallersCancellationAsNotFailed(t *testing.T) {
	for _, tt := range callEndings() {
		t.Run(tt.name, func(t *testing.T) {
			recorder := recordToolSpans(t)
			tools := wrapToolTracing([]toolexec.Tool{{
				Name: "exec",
				Execute: func(execCtx *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
					return sdk.ToolOutput{}, execCtx.Err()
				},
			}})

			_, _ = tools[0].Execute(&toolexec.ToolExecContext{Context: endedContext(tt.cause)}, toolexec.ArgumentsFromValue(nil))

			wantCode := codes.Unset
			if tt.wantFailed {
				wantCode = codes.Error
			}
			if got := findSpan(t, recorder, "agent.tool exec").Status().Code; got != wantCode {
				t.Fatalf("status = %v, want %v", got, wantCode)
			}
		})
	}
}
