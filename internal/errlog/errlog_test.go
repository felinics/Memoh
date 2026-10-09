package errlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	grpccodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/logger"
)

// clientErr is a catalog 4xx.
var clientErr = apperror.New(apperror.CodeCapabilityNotFound, nil)

func TestFinishLevels(t *testing.T) {
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	internalCtx, cancelInternal := context.WithCancelCause(context.Background())
	cancelInternal(errors.New("run ownership lost"))
	cases := []struct {
		name  string
		err   error
		ctx   context.Context
		opts  Options
		level slog.Level
		fault apperror.Fault
		panic bool
	}{
		{name: "server", err: errs.New("server"), level: slog.LevelError, fault: apperror.FaultServer},
		{name: "dependency", err: errs.NewDependency("dependency"), level: slog.LevelError, fault: apperror.FaultDependency},
		{name: "retry", err: errs.NewDependency("dependency"), opts: Options{WillRetry: true}, level: slog.LevelWarn, fault: apperror.FaultDependency},
		{name: "client", err: clientErr, level: slog.LevelInfo, fault: apperror.FaultClient},
		{name: "async_client", err: clientErr, opts: Options{Async: true}, level: slog.LevelError, fault: apperror.FaultServer},
		{name: "canceled", ctx: canceledCtx, err: context.Canceled, level: slog.LevelInfo, fault: apperror.FaultCanceled},
		{name: "internal_cancel", ctx: internalCtx, err: context.Canceled, level: slog.LevelError, fault: apperror.FaultServer},
		{name: "panic", err: recovered(), opts: Options{WillRetry: true}, level: slog.LevelError, fault: apperror.FaultServer, panic: true},
		{name: "recorded_server", err: errs.Recorded(errs.Wrap(errs.New("run failed"), "trigger")), level: slog.LevelWarn, fault: apperror.FaultServer},
		{name: "recorded_dependency", err: errs.Wrap(errs.Recorded(errs.NewDependency("provider down")), "trigger"), level: slog.LevelWarn, fault: apperror.FaultDependency},
		{name: "recorded_async_client", err: errs.Recorded(clientErr), opts: Options{Async: true}, level: slog.LevelWarn, fault: apperror.FaultServer},
		{name: "recorded_client", err: errs.Recorded(clientErr), level: slog.LevelInfo, fault: apperror.FaultClient},
		{name: "recorded_panic", err: errs.Recorded(recovered()), level: slog.LevelError, fault: apperror.FaultServer, panic: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := tc.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			got := Finish(ctx, tc.name, tc.err, tc.opts)
			if got.Level != tc.level || got.Report.Fault != tc.fault {
				t.Fatalf("got level=%v fault=%q, want level=%v fault=%q", got.Level, got.Report.Fault, tc.level, tc.fault)
			}
			if tc.name == "async_client" && !got.Report.Unlocated {
				t.Fatal("async client without stack must be unlocated")
			}
			if got.Report.Panic != tc.panic {
				t.Fatalf("panic=%v, want %v", got.Report.Panic, tc.panic)
			}
		})
	}
}

func TestRemoteLevels(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault string
		level slog.Level
	}{
		{"remote server", "server", slog.LevelWarn},
		{"remote dependency", "dependency", slog.LevelWarn},
		{"remote fault absent", "", slog.LevelError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := errs.Remote(remoteStatus(t, tc.fault))
			if got := Finish(context.Background(), "rpc", err, Options{}); got.Level != tc.level || got.Report.Fault != apperror.FaultDependency {
				t.Fatalf("level=%v fault=%q", got.Level, got.Report.Fault)
			}
		})
	}
}

func TestFinishSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tracer := provider.Tracer("errlog-test")

	ctx, span := tracer.Start(context.Background(), "unit")
	result := Finish(ctx, "unit", errs.New("broken"), Options{})
	span.End()
	if result.Report.Reason != "internal" {
		t.Fatalf("reason=%q", result.Report.Reason)
	}
	ended := recorder.Ended()
	if len(ended) != 1 || ended[0].Status().Code != codes.Error || ended[0].Status().Description != "internal" {
		t.Fatalf("span status=%v, want error internal", ended[0].Status())
	}
	attrs := ended[0].Attributes()
	if len(attrs) != 1 || attrs[0] != attribute.String("error.type", "internal") {
		t.Fatalf("span attributes=%v", attrs)
	}

	ctx, span = tracer.Start(context.Background(), "client")
	Finish(ctx, "client", clientErr, Options{})
	span.End()
	client := recorder.Ended()[1]
	if client.Status().Code != codes.Unset {
		t.Fatalf("client span status=%v, want unset", client.Status())
	}
	if attrs := client.Attributes(); len(attrs) != 1 || attrs[0] != attribute.String("error.type", string(apperror.CodeCapabilityNotFound)) {
		t.Fatalf("client span attributes=%v", attrs)
	}
}

func TestUnlocatedMetric(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	old := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(old); _ = provider.Shutdown(context.Background()) })

	Finish(context.Background(), "job.test", errors.New("bare"), Options{})
	Finish(context.Background(), "job.located", errs.New("located"), Options{})
	Finish(context.Background(), "job.client", clientErr, Options{})
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, scope := range data.ScopeMetrics {
		if scope.Scope.Name != meterName {
			continue
		}
		for _, m := range scope.Metrics {
			if m.Name != "errors.unlocated_total" {
				continue
			}
			found = true
			points := m.Data.(metricdata.Sum[int64]).DataPoints
			if len(points) != 1 {
				t.Fatalf("points=%v, want only the unlocated operation", points)
			}
			operation, ok := points[0].Attributes.Value("operation")
			if points[0].Value != 1 || !ok || operation.AsString() != "job.test" {
				t.Fatalf("points=%v", points)
			}
		}
	}
	if !found {
		t.Fatal("unlocated metric not found")
	}
}

// The boundary writes the record through the process logger; the error fields
// sit next to the correlation fields the logger adds, at the top level.
func TestAttrsThroughProcessLogger(t *testing.T) {
	provider := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	ctx, span := provider.Tracer("errlog-test").Start(logger.ContextWithRequestID(context.Background(), "req-1"), "unit")
	defer span.End()

	result := Finish(ctx, "unit", errs.Wrap(errors.New("failure"), "wrapped", slog.String("bot_id", "b1")), Options{Async: true})
	var out bytes.Buffer
	log := logger.New(&out, "debug", "json").With(slog.String("component", "test"))
	log.LogAttrs(ctx, result.Level, "unit failed", result.Attrs()...)

	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["level"] != "ERROR" || got["fault"] != "server" || got["reason"] != "internal" || got["error"] != "wrapped: failure" || got["will_retry"] != false {
		t.Fatalf("record=%v", got)
	}
	if got["request_id"] != "req-1" || got["trace_id"] != span.SpanContext().TraceID().String() {
		t.Fatalf("correlation fields missing: %v", got)
	}
	if attrs, ok := got["error_attrs"].(map[string]any); !ok || attrs["bot_id"] != "b1" {
		t.Fatalf("error_attrs=%v", got["error_attrs"])
	}
	if _, ok := got["error_source"].(map[string]any); !ok {
		t.Fatalf("error_source=%v", got["error_source"])
	}
	if _, ok := got["source"]; ok {
		t.Fatal("slog's own source key must stay unused")
	}
}

func TestEventLeavesSpanUnset(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	ctx, span := provider.Tracer("errlog-test").Start(context.Background(), "unit")
	result := Event(ctx, "unit", errs.New("mark failed"), Options{Async: true})
	span.End()
	if result.Level != slog.LevelWarn || result.Report.Fault != apperror.FaultServer {
		t.Fatalf("level=%v fault=%q", result.Level, result.Report.Fault)
	}
	ended := recorder.Ended()
	if ended[0].Status().Code != codes.Unset || len(ended[0].Attributes()) != 0 {
		t.Fatalf("event line touched span: status=%v attrs=%v", ended[0].Status(), ended[0].Attributes())
	}
}

func TestResultAttrsOmitErrorFieldsOnSuccess(t *testing.T) {
	for _, opts := range []Options{{}, {Async: true}, {Async: true, WillRetry: true}} {
		if got := Finish(context.Background(), "unit", nil, opts); len(got.Attrs()) != 0 || got.Level != slog.LevelInfo {
			t.Errorf("opts=%+v: success row carries %v at %v", opts, got.Attrs(), got.Level)
		}
	}
}

func TestResultAttrsWillRetry(t *testing.T) {
	failure := errs.New("failed")
	cases := []struct {
		name   string
		result Result
		want   any // nil = field absent
	}{
		{"async retry", Finish(context.Background(), "unit", failure, Options{Async: true, WillRetry: true}), true},
		{"async final", Finish(context.Background(), "unit", failure, Options{Async: true}), false},
		{"sync", Finish(context.Background(), "unit", failure, Options{WillRetry: true}), nil},
		{"event", Event(context.Background(), "unit", failure, Options{Async: true, WillRetry: true}), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got any
			for _, attr := range tc.result.Attrs() {
				if attr.Key == "will_retry" {
					got = attr.Value.Bool()
				}
			}
			if got != tc.want {
				t.Fatalf("will_retry = %v, want %v; attrs=%v", got, tc.want, tc.result.Attrs())
			}
		})
	}
}

func recovered() (err error) {
	defer func() {
		err = errs.Recovered(recover())
	}()
	panic("boom")
}

func remoteStatus(t *testing.T, fault string) error {
	t.Helper()
	info := &errdetails.ErrorInfo{Reason: "remote.failed", Metadata: map[string]string{}}
	if fault != "" {
		info.Metadata["fault"] = fault
	}
	st, err := status.New(grpccodes.Internal, "remote failed").WithDetails(info)
	if err != nil {
		t.Fatal(err)
	}
	return st.Err()
}
