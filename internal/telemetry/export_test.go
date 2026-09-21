package telemetry

import "github.com/felinics/memoh/internal/config"

// UseInsecureForTest exposes the transport-security decision. It is the one
// piece of exporter assembly worth asserting on directly: the alternative is
// to stand up a TLS and a plaintext collector to observe which one a build
// talks to.
func UseInsecureForTest(cfg config.TelemetryConfig) bool { return useInsecure(cfg) }
