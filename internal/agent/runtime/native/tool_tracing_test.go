package native

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felinics/twilight/sdk"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func recordToolSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	return recorder
}

func findSpan(t *testing.T, recorder *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range recorder.Ended() {
		if span.Name() == name {
			return span
		}
	}
	t.Fatalf("no span named %q; got %v", name, spanNames(recorder))
	return nil
}

func spanNames(recorder *tracetest.SpanRecorder) []string {
	var out []string
	for _, span := range recorder.Ended() {
		out = append(out, span.Name())
	}
	return out
}

func TestWrapToolTracingRecordsACallWithoutItsArguments(t *testing.T) {
	// A tool call carries file contents, shell commands and whatever the user
	// asked for. Recording the input would put all of it in the trace
	// backend, which is exactly what docs/logging.md keeps out of records.
	recorder := recordToolSpans(t)
	tools := wrapToolTracing([]sdk.Tool{{
		Name: "bash",
		Execute: func(*sdk.ToolExecContext, any) (any, error) {
			return "ok", nil
		},
	}})

	out, err := tools[0].Execute(&sdk.ToolExecContext{Context: context.Background()},
		map[string]any{"command": "cat /etc/synthetic-secret"})
	if err != nil || out != "ok" {
		t.Fatalf("Execute() = %v, %v", out, err)
	}

	span := findSpan(t, recorder, "agent.tool bash")
	for _, kv := range span.Attributes() {
		if strings.Contains(kv.Value.Emit(), "synthetic-secret") {
			t.Errorf("attribute %s recorded the tool input: %s", kv.Key, kv.Value.Emit())
		}
	}
	if span.SpanKind() != trace.SpanKindInternal {
		t.Errorf("span kind = %v, want internal", span.SpanKind())
	}
}

// The tool's own work — a workspace RPC, a database read — has to hang
// beneath its span. If the wrapper does not put the span back on the context
// the tool reads, those spans float at the root of the trace and the waterfall
// no longer says which tool caused them.
func TestWrapToolTracingGivesTheToolItsOwnSpanContext(t *testing.T) {
	recorder := recordToolSpans(t)
	tools := wrapToolTracing([]sdk.Tool{{
		Name: "browser_action",
		Execute: func(execCtx *sdk.ToolExecContext, _ any) (any, error) {
			_, inner := otel.Tracer("test").Start(execCtx.Context, "inner.work")
			inner.End()
			return "ok", nil
		},
	}})

	if _, err := tools[0].Execute(&sdk.ToolExecContext{Context: context.Background()}, nil); err != nil {
		t.Fatal(err)
	}

	tool := findSpan(t, recorder, "agent.tool browser_action")
	inner := findSpan(t, recorder, "inner.work")
	if inner.Parent().SpanID() != tool.SpanContext().SpanID() {
		t.Errorf("inner span parent = %s, want the tool span %s",
			inner.Parent().SpanID(), tool.SpanContext().SpanID())
	}
}

func TestWrapToolTracingMarksAFailedCall(t *testing.T) {
	recorder := recordToolSpans(t)
	tools := wrapToolTracing([]sdk.Tool{{
		Name: "write_file",
		Execute: func(*sdk.ToolExecContext, any) (any, error) {
			return nil, errors.New("disk full")
		},
	}})

	if _, err := tools[0].Execute(&sdk.ToolExecContext{Context: context.Background()}, nil); err == nil {
		t.Fatal("want the tool's error to pass through")
	}
	if span := findSpan(t, recorder, "agent.tool write_file"); span.Status().Code != codes.Error {
		t.Error("a failed tool call was not marked as an error")
	}
}

func TestWrapToolTracingLeavesTheCallersSliceAlone(t *testing.T) {
	// The decorators in this chain copy before they wrap. Mutating the input
	// would double-wrap whichever caller reuses the assembled slice.
	original := []sdk.Tool{{Name: "read_file", Execute: func(*sdk.ToolExecContext, any) (any, error) { return nil, nil }}}
	wrapped := wrapToolTracing(original)
	if &original[0] == &wrapped[0] {
		t.Fatal("wrapToolTracing returned the caller's slice")
	}
	recorder := recordToolSpans(t)
	if _, err := original[0].Execute(&sdk.ToolExecContext{Context: context.Background()}, nil); err != nil {
		t.Fatal(err)
	}
	if names := spanNames(recorder); len(names) != 0 {
		t.Errorf("the original tool was instrumented in place: %v", names)
	}
}
