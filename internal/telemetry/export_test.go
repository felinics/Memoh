package telemetry

import (
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/felinics/memoh/internal/config"
)

// UseInsecureForTest exposes the transport-security decision. It is the one
// piece of exporter assembly worth asserting on directly: the alternative is
// to stand up a TLS and a plaintext collector to observe which one a build
// talks to.
func UseInsecureForTest(cfg config.TelemetryConfig) bool { return useInsecure(cfg) }

// MeterProviderForTest is the meter provider Setup installs, reading into
// the given reader instead of exporting.
func MeterProviderForTest(reader sdkmetric.Reader) *sdkmetric.MeterProvider {
	return newMeterProvider(reader, resource.Empty())
}

// ResourceForTest is the resource Setup attaches to every span and metric.
func ResourceForTest(svc Service) *resource.Resource {
	return newResource(config.TelemetryConfig{}, svc)
}
