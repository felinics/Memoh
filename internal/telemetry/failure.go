package telemetry

import (
	"fmt"
	"runtime"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// RecordFailure identifies the error observation boundary, not its origin.
// Error strings may contain provider responses or tool inputs; record the
// concrete type instead. Returned errors remain unchanged.
func RecordFailure(span trace.Span, err error) {
	if err == nil || !span.IsRecording() {
		return
	}
	attrs := observationLocation()
	attrs = append(attrs, attribute.String("exception.type", fmt.Sprintf("%T", err)),
		attribute.String("exception.message", "operation failed"),
		attribute.String("error.location.kind", "observation"))
	span.AddEvent("exception", trace.WithAttributes(attrs...))
	span.SetStatus(codes.Error, "")
}

// RecordPanic must run while the panicking stack is still present. The caller
// remains responsible for recovery or re-panicking; this only records evidence.
func RecordPanic(span trace.Span, value any) {
	if !span.IsRecording() {
		return
	}
	var pcs [64]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	var stack strings.Builder
	for {
		frame, more := frames.Next()
		fmt.Fprintf(&stack, "%s\n\t%s:%d\n", frame.Function, frame.File, frame.Line)
		if !more {
			break
		}
	}
	span.AddEvent("exception", trace.WithAttributes(
		attribute.String("exception.type", fmt.Sprintf("%T", value)),
		attribute.String("exception.message", "panic"),
		attribute.String("exception.stacktrace", stack.String()),
		attribute.Bool("exception.escaped", true),
		attribute.String("error.location.kind", "panic_stack"),
	))
	span.SetStatus(codes.Error, "")
}

func observationLocation() []attribute.KeyValue {
	pc, file, line, ok := runtime.Caller(2)
	if !ok {
		return nil
	}
	function := ""
	if fn := runtime.FuncForPC(pc); fn != nil {
		function = fn.Name()
	}
	return []attribute.KeyValue{
		attribute.String("code.function.name", function),
		attribute.String("code.file.path", file),
		attribute.Int("code.line.number", line),
	}
}
