package telemetry_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/telemetry"
)

func discard() *slog.Logger { return slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)) }

func TestSetupWithoutAnEndpointInstallsNoProvider(t *testing.T) {
	// The whole point of "off by default": no exporter is constructed, so a
	// deployment with no collector is not retrying a connection forever.
	//
	// The assertion is that the global provider is left exactly as it was,
	// rather than that a span does not record. Those mean the same thing in a
	// fresh process but not in a test binary: otel's global tracers delegate
	// permanently to the first real provider installed, so any earlier test
	// that installed one would make a recording assertion depend on test
	// order.
	before, meters := otel.GetTracerProvider(), otel.GetMeterProvider()
	shutdown, err := telemetry.Setup(context.Background(), config.TelemetryConfig{}, telemetry.Service{Name: "test"}, discard())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	if otel.GetTracerProvider() != before {
		t.Error("a tracer provider was installed with no collector configured")
	}
	if otel.GetMeterProvider() != meters {
		t.Error("a meter provider was installed with no collector configured")
	}
}

func TestSetupInstallsPropagationEvenWhenExportIsOff(t *testing.T) {
	// Propagation is unconditional. Without it, a deployment that enables
	// tracing in one service gets a disconnected trace per hop, and the
	// missing piece is invisible: every service produces spans, they just
	// never join up.
	shutdown, err := telemetry.Setup(context.Background(), config.TelemetryConfig{}, telemetry.Service{Name: "test"}, discard())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	carrier := propagation.MapCarrier{
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), carrier)
	sc := trace.SpanContextFromContext(ctx)
	if got := sc.TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace id = %q, want the one from the header", got)
	}
	if !sc.IsRemote() {
		t.Error("extracted span context is not marked remote")
	}
}

func TestSetupRejectsAnUnknownProtocol(t *testing.T) {
	// A typo in the protocol must not silently fall back to a transport the
	// operator did not ask for and then fail to connect for a different
	// reason than the one they would look for.
	_, err := telemetry.Setup(context.Background(), config.TelemetryConfig{
		Endpoint: "collector:4317",
		Protocol: "htpp",
	}, telemetry.Service{Name: "test"}, discard())
	if err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
}

func TestTracerIsUsableBeforeSetup(t *testing.T) {
	// Anything constructed during startup may trace before Setup has run.
	// The global no-op has to carry it: a nil span here would be a panic in
	// whichever constructor happened to be wired first.
	ctx, span := telemetry.Tracer().Start(context.Background(), "early")
	span.End()
	if trace.SpanFromContext(ctx) == nil {
		t.Fatal("no span in the context returned before setup")
	}
}

func TestSchemeDecidesTransportSecurity(t *testing.T) {
	// The OTLP specification makes the scheme authoritative, and the failure
	// when it is not is unreadable: an exporter that negotiates TLS with a
	// plaintext collector reports a handshake error naming neither the
	// setting that caused it nor the one that fixes it.
	for _, tc := range []struct {
		name         string
		endpoint     string
		configured   bool
		wantInsecure bool
	}{
		{"http scheme overrides a secure config", "http://collector:4317", false, true},
		{"https scheme overrides an insecure config", "https://collector:4317", true, false},
		{"no scheme keeps the configured value", "collector:4317", true, true},
		{"no scheme defaults to TLS", "collector:4317", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.TelemetryConfig{Endpoint: tc.endpoint, Insecure: tc.configured}
			if got := telemetry.UseInsecureForTest(cfg); got != tc.wantInsecure {
				t.Errorf("insecure = %v, want %v", got, tc.wantInsecure)
			}
		})
	}
}

func TestSetupDoesNotForwardBaggage(t *testing.T) {
	// Nothing here writes baggage, so installing the propagator would only
	// make this process carry a caller's header onward — through the internal
	// RPC and into the per-bot workspace container. A value we neither
	// produce nor read is a channel we cannot account for.
	shutdown, err := telemetry.Setup(context.Background(), config.TelemetryConfig{}, telemetry.Service{Name: "test"}, discard())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	in := propagation.MapCarrier{
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"baggage":     "secret=synthetic-value",
	}
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), in)
	out := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, out)

	if got := out["baggage"]; got != "" {
		t.Errorf("baggage forwarded: %q", got)
	}
	if out["traceparent"] == "" {
		t.Error("traceparent was not propagated")
	}
}

func TestStartupLineHidesCredentialsInTheEndpoint(t *testing.T) {
	// An operator may legitimately configure http://user:pass@collector:4317.
	// A startup line is read by everyone who can read logs.
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	endpoint := (&url.URL{
		Scheme: "http",
		User:   url.UserPassword("someone", "synthetic-password"),
		Host:   "collector:4317",
	}).String()
	shutdown, err := telemetry.Setup(context.Background(), config.TelemetryConfig{
		Endpoint: endpoint,
	}, telemetry.Service{Name: "test"}, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownUnreachable(shutdown) })

	if strings.Contains(buf.String(), "synthetic-password") {
		t.Errorf("the startup line published the endpoint's password: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "collector:4317") {
		t.Errorf("the startup line does not say where it is exporting: %s", buf.String())
	}
}

// shutdownUnreachable shuts down a Setup whose collector does not exist.
// Shutdown flushes the metric reader, and that export would otherwise wait
// out the exporter's own timeout.
func shutdownUnreachable(shutdown telemetry.Shutdown) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = shutdown(ctx)
}

// keepGlobalProviders puts back the providers a test's Setup replaces, so
// the next test starts from the state it would in a fresh process.
func keepGlobalProviders(t *testing.T) {
	t.Helper()
	tracers, meters := otel.GetTracerProvider(), otel.GetMeterProvider()
	t.Cleanup(func() {
		otel.SetTracerProvider(tracers)
		otel.SetMeterProvider(meters)
	})
}

func TestSetupWithAnEndpointInstallsBothProviders(t *testing.T) {
	keepGlobalProviders(t)
	tracers, meters := otel.GetTracerProvider(), otel.GetMeterProvider()
	shutdown, err := telemetry.Setup(context.Background(), config.TelemetryConfig{
		Endpoint: "http://127.0.0.1:1",
	}, telemetry.Service{Name: "test"}, discard())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownUnreachable(shutdown) })

	if otel.GetTracerProvider() == tracers {
		t.Error("no tracer provider was installed")
	}
	if otel.GetMeterProvider() == meters {
		t.Error("no meter provider was installed")
	}
}

func TestMetricsCanBeTurnedOffWithoutTurningOffTraces(t *testing.T) {
	// OTEL_METRICS_EXPORTER=none is the standard switch for a collector that
	// accepts traces and rejects metrics. Without it the only way to stop the
	// rejected exports is to stop tracing as well.
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	keepGlobalProviders(t)
	tracers, meters := otel.GetTracerProvider(), otel.GetMeterProvider()
	shutdown, err := telemetry.Setup(context.Background(), config.TelemetryConfig{
		Endpoint: "http://127.0.0.1:1",
	}, telemetry.Service{Name: "test"}, discard())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownUnreachable(shutdown) })

	if otel.GetMeterProvider() != meters {
		t.Error("a meter provider was installed with OTEL_METRICS_EXPORTER=none")
	}
	if otel.GetTracerProvider() == tracers {
		t.Fatal("OTEL_METRICS_EXPORTER=none also turned off traces")
	}
	_, span := otel.Tracer("test").Start(context.Background(), "after setup")
	defer span.End()
	if !span.SpanContext().IsValid() {
		t.Error("the installed tracer provider does not produce spans")
	}
}

func TestRequestDurationBucketsReachFiveMinutes(t *testing.T) {
	// The SDK's default buckets are for milliseconds and put every request
	// over 10 ms in the same few buckets, so a chat turn that takes a minute
	// has no quantile at all.
	reader := sdkmetric.NewManualReader()
	provider := telemetry.MeterProviderForTest(reader)
	histogram, err := provider.Meter("test").Float64Histogram("http.server.request.duration")
	if err != nil {
		t.Fatal(err)
	}
	histogram.Record(context.Background(), 45)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	if len(rm.ScopeMetrics) != 1 || len(rm.ScopeMetrics[0].Metrics) != 1 {
		t.Fatalf("want one metric, got %+v", rm.ScopeMetrics)
	}
	points := rm.ScopeMetrics[0].Metrics[0].Data.(metricdata.Histogram[float64]).DataPoints
	want := []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10, 15, 30, 60, 120, 300}
	if !slices.Equal(points[0].Bounds, want) {
		t.Errorf("bounds = %v, want %v", points[0].Bounds, want)
	}
}

func TestShutdownDeliversPendingSpansAndMetrics(t *testing.T) {
	// Both signals are batched. A pod that is stopped between two exports
	// loses whatever it had collected unless shutdown sends it.
	var mu sync.Mutex
	paths := map[string]int{}
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths[r.URL.Path]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)

	keepGlobalProviders(t)
	shutdown, err := telemetry.Setup(context.Background(), config.TelemetryConfig{
		Endpoint:    collector.URL,
		Protocol:    config.TelemetryProtocolHTTP,
		SampleRatio: 1,
	}, telemetry.Service{Name: "test"}, discard())
	if err != nil {
		t.Fatal(err)
	}
	_, span := otel.Tracer("test").Start(context.Background(), "pending")
	span.End()
	counter, err := otel.Meter("test").Int64Counter("pending")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(context.Background(), 1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if paths["/v1/traces"] != 1 || paths["/v1/metrics"] != 1 {
		t.Errorf("want one trace export and one metric export, got %v", paths)
	}
}

func TestEveryProcessHasItsOwnInstanceID(t *testing.T) {
	instanceID := func(svc telemetry.Service) string {
		v, ok := telemetry.ResourceForTest(svc).Set().Value(semconv.ServiceInstanceIDKey)
		if !ok {
			t.Fatalf("resource for %+v has no service.instance.id", svc)
		}
		return v.AsString()
	}

	first := instanceID(telemetry.Service{Name: "memoh-server"})
	second := instanceID(telemetry.Service{Name: "memoh-server"})
	if first == "" || first == second {
		t.Fatalf("unconfigured instance ids = %q, %q, want two distinct values", first, second)
	}
	if got := instanceID(telemetry.Service{Name: "memoh-server", InstanceID: " node-a "}); got != "node-a" {
		t.Fatalf("configured instance id = %q, want node-a", got)
	}
}
