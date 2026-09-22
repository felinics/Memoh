package telemetry_test

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestFailureObservationLocationAndPrivacy(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	_, span := provider.Tracer("test").Start(context.Background(), "test")
	_, file, line, _ := runtime.Caller(0)
	telemetry.RecordFailure(span, errors.New("secret-provider-response"))
	span.End()
	got := recorder.Ended()[0]
	if got.Status().Code != codes.Error || len(got.Events()) != 1 {
		t.Fatalf("failure event/status missing: %v", got.Events())
	}
	attrs := attribute.NewSet(got.Events()[0].Attributes...)
	for key, want := range map[attribute.Key]string{
		"code.file.path": file, "error.location.kind": "observation",
		"exception.message": "operation failed",
	} {
		value, _ := attrs.Value(key)
		if value.AsString() != want {
			t.Errorf("%s = %v, want %s", key, value, want)
		}
	}
	location, _ := attrs.Value("code.line.number")
	if location.AsInt64() != int64(line+1) {
		t.Errorf("line = %d, want %d", location.AsInt64(), line+1)
	}
}

func TestPanicStackHasCodeButNotPanicValue(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	_, span := provider.Tracer("test").Start(context.Background(), "test")
	func() {
		defer func() {
			if value := recover(); value != nil {
				telemetry.RecordPanic(span, value)
			}
		}()
		panic("secret-panic-payload")
	}()
	span.End()
	event := recorder.Ended()[0].Events()[0]
	attrs := attribute.NewSet(event.Attributes...)
	stack, _ := attrs.Value("exception.stacktrace")
	if !strings.Contains(stack.AsString(), "failure_test.go:") {
		t.Fatal("panic stack has no source location")
	}
	for _, attr := range event.Attributes {
		if strings.Contains(attr.Value.Emit(), "secret-panic-payload") {
			t.Fatal("panic payload leaked")
		}
	}
}
