package telemetry_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
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
	before := otel.GetTracerProvider()
	shutdown, err := telemetry.Setup(context.Background(), config.TelemetryConfig{}, telemetry.Service{Name: "test"}, discard())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	if otel.GetTracerProvider() != before {
		t.Error("a tracer provider was installed with no collector configured")
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
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	if strings.Contains(buf.String(), "synthetic-password") {
		t.Errorf("the startup line published the endpoint's password: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "collector:4317") {
		t.Errorf("the startup line does not say where it is exporting: %s", buf.String())
	}
}
