// Package telemetry assembles OpenTelemetry tracing for a Memoh process.
//
// Tracing answers the question logs cannot: where the time went, and which
// step of a request failed. A record says "workspace snapshot failed"; a trace
// says the turn spent eleven seconds waiting for the model and then failed on
// the third tool call.
//
// Nothing here is on by default. With no collector configured the process
// installs no provider at all, leaving OpenTelemetry's global no-op in place,
// and the only thing this package contributes is context propagation — which
// costs nothing and must be unconditional (see Setup).
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/version"
)

// ScopeName identifies spans this repository creates, as opposed to spans from
// an instrumentation library.
const ScopeName = "github.com/felinics/memoh"

// Service describes the process being traced.
type Service struct {
	// Name is the service.name every span is attributed to: "memoh-server",
	// "memoh-channel". A backend groups by it, so it has to be stable.
	Name string
	// InstanceID distinguishes replicas of the same service. Optional.
	InstanceID string
}

// Shutdown flushes pending spans and releases the exporter.
type Shutdown func(context.Context) error

// Tracer returns the tracer for spans created by this repository's own code.
// Before Setup — or with tracing disabled — this is the global no-op.
func Tracer() trace.Tracer {
	return otel.Tracer(ScopeName)
}

// Setup installs context propagation and, when a collector is configured, a
// tracer provider exporting to it. The returned Shutdown is always safe to
// call.
//
// Propagation is installed even when export is off, and that is deliberate.
// A propagator only reads and writes the traceparent header; with no provider
// there is no span to write, so the cost is a map lookup on a header that is
// not there. What it buys is that the decision is not baked into the binary:
// a deployment that turns on tracing in one service gets continuous traces
// across the others rather than a separate disconnected trace per hop, and
// nobody has to discover that propagation was the missing piece.
func Setup(ctx context.Context, cfg config.TelemetryConfig, svc Service, log *slog.Logger) (Shutdown, error) {
	// TraceContext only. Baggage would make this process a forwarder for
	// whatever a caller puts in the header: nothing here writes baggage, but
	// installing the propagator would carry an inbound `baggage:` through the
	// internal RPC and on into the per-bot workspace container. Propagating
	// values we neither produce nor read is a channel we would not be able to
	// account for.
	otel.SetTextMapPropagator(propagation.TraceContext{})

	if !cfg.Enabled() {
		return func(context.Context) error { return nil }, nil
	}

	// SDK-internal failures — an export that could not be delivered, a
	// malformed attribute — otherwise go to stderr outside the log stream.
	// They are warnings: losing telemetry is not losing work.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		// This handler outlives Setup; ctx here is the startup context, and an
		// export failure an hour later does not belong to it.
		//logctx:plain
		log.Warn("opentelemetry", slog.Any("error", err), slog.String("endpoint", safeEndpoint(cfg.Endpoint)))
	}))

	exporter, err := newExporter(ctx, cfg)
	if err != nil {
		return nil, err
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(newResource(cfg, svc)),
		// ParentBased: a request that arrives already sampled stays sampled,
		// whatever the local ratio says. Deciding locally would cut traces in
		// half at process boundaries, which is worse than either keeping or
		// dropping them whole.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(clampRatio(cfg.SampleRatio)))),
	)
	otel.SetTracerProvider(provider)

	// tls is reported because it is the setting most likely to be wrong and
	// the one whose failure says least: an exporter talking TLS to a
	// plaintext collector reports a handshake error, not a configuration
	// problem.
	//logctx:plain
	log.Info("tracing enabled",
		slog.String("endpoint", safeEndpoint(cfg.Endpoint)),
		slog.String("protocol", protocolOf(cfg)),
		slog.Bool("tls", !useInsecure(cfg)),
		slog.Float64("sample_ratio", clampRatio(cfg.SampleRatio)),
		slog.String("service_name", serviceName(cfg, svc)),
	)

	return provider.Shutdown, nil
}

func newExporter(ctx context.Context, cfg config.TelemetryConfig) (*otlptrace.Exporter, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	insecure := useInsecure(cfg)
	switch protocolOf(cfg) {
	case config.TelemetryProtocolHTTP:
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(withScheme(endpoint, insecure))}
		if insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlptracehttp.WithHeaders(cfg.Headers))
		}
		return otlptracehttp.New(ctx, opts...)
	case config.TelemetryProtocolGRPC:
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(hostPort(endpoint))}
		if insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlptracegrpc.WithHeaders(cfg.Headers))
		}
		return otlptracegrpc.New(ctx, opts...)
	default:
		return nil, fmt.Errorf("telemetry: unknown protocol %q, want %q or %q",
			cfg.Protocol, config.TelemetryProtocolGRPC, config.TelemetryProtocolHTTP)
	}
}

// useInsecure decides transport security the way the OTLP specification does:
// the endpoint's scheme wins when it has one.
//
// The scheme has to win, because the failure when it does not is unreadable.
// An operator who writes the address of a plaintext in-cluster collector as
// http://collector:4317 and leaves `insecure` alone would otherwise get a TLS
// handshake error fifteen seconds after startup, naming neither the setting
// that caused it nor the one that fixes it.
//
// An endpoint with no scheme carries no such instruction, so the config value
// stands. Setup logs which way it went for exactly that case.
func useInsecure(cfg config.TelemetryConfig) bool {
	// Lowercased first: a scheme is case-insensitive, and "HTTP://collector"
	// matching neither branch would quietly negotiate TLS against a plaintext
	// collector and report a handshake error instead of a configuration one.
	switch endpoint := strings.ToLower(strings.TrimSpace(cfg.Endpoint)); {
	case strings.HasPrefix(endpoint, "http://"):
		return true
	case strings.HasPrefix(endpoint, "https://"):
		return false
	default:
		return cfg.Insecure
	}
}

// safeEndpoint renders a collector address for a log record. A URL may carry
// credentials — http://user:pass@collector:4317 is a valid thing for an
// operator to configure — and a startup line is read by everyone who can read
// logs.
func safeEndpoint(endpoint string) string {
	// A bare host:port is a valid thing to configure and is not a URL, so
	// url.Parse leaves Host empty for it and the parsed form cannot be
	// trusted to have found the credentials. Parsing against a synthetic
	// scheme makes "user:pass@collector:4317" and "collector:4317" take the
	// same path — the first is exactly the input that would otherwise fall
	// through unredacted.
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		parsed, err = url.Parse("scheme://" + endpoint)
		if err != nil || parsed.Host == "" {
			// Not addressable either way. Anything before an @ is the part
			// that could be a credential.
			if _, after, found := strings.Cut(endpoint, "@"); found {
				return after
			}
			return endpoint
		}
		parsed.User = nil
		parsed.RawQuery = ""
		return strings.TrimPrefix(parsed.String(), "scheme://")
	}
	parsed.User = nil
	parsed.RawQuery = ""
	return parsed.String()
}

// hostPort strips a scheme the grpc exporter does not want; it takes a bare
// authority, unlike the http one.
func hostPort(endpoint string) string {
	if parsed, err := url.Parse(endpoint); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return endpoint
}

// withScheme supplies the scheme the http exporter needs to build a URL when
// the endpoint was written as a bare host:port.
func withScheme(endpoint string, insecure bool) string {
	if strings.Contains(endpoint, "://") {
		return endpoint
	}
	if insecure {
		return "http://" + endpoint
	}
	return "https://" + endpoint
}

func newResource(cfg config.TelemetryConfig, svc Service) *resource.Resource {
	attrs := []attribute.KeyValue{
		semconv.ServiceName(serviceName(cfg, svc)),
		semconv.ServiceVersion(version.Version),
	}
	if id := strings.TrimSpace(svc.InstanceID); id != "" {
		attrs = append(attrs, semconv.ServiceInstanceID(id))
	}
	// resource.Merge reports a schema-URL conflict as an error while still
	// returning a usable resource; the merged result is what we want either
	// way, so the mismatch is not worth failing startup over.
	merged, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL, attrs...))
	if err != nil {
		if errors.Is(err, resource.ErrSchemaURLConflict) && merged != nil {
			return merged
		}
		return resource.NewWithAttributes(semconv.SchemaURL, attrs...)
	}
	return merged
}

func serviceName(cfg config.TelemetryConfig, svc Service) string {
	if name := strings.TrimSpace(cfg.ServiceName); name != "" {
		return name
	}
	return svc.Name
}

func protocolOf(cfg config.TelemetryConfig) string {
	if protocol := strings.TrimSpace(cfg.Protocol); protocol != "" {
		return protocol
	}
	return config.TelemetryProtocolGRPC
}

// clampRatio keeps an out-of-range ratio from silently meaning its opposite.
// TraceIDRatioBased treats anything >= 1 as "always" and <= 0 as "never", so a
// typo like 100 (meaning percent) would read as always-on and -1 as off.
func clampRatio(ratio float64) float64 {
	switch {
	case ratio < 0:
		return 0
	case ratio > 1:
		return 1
	default:
		return ratio
	}
}
