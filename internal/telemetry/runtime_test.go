package telemetry_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/felinics/memoh/internal/telemetry"
)

func metricNames(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]metricdata.Metrics{}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

// The runtime panels read these names; they are the ones every other Go
// service in a deployment reports, so one query covers all of them.
func TestRuntimeMetricsUseTheSemanticConventionNames(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := telemetry.MeterProviderForTest(reader)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	if err := telemetry.StartRuntimeMetricsForTest(provider); err != nil {
		t.Fatal(err)
	}

	names := metricNames(t, reader)
	for _, want := range []string{"go.goroutine.count", "go.memory.used", "go.memory.gc.goal"} {
		if _, ok := names[want]; !ok {
			t.Errorf("%s not recorded; got %v", want, keys(names))
		}
	}
}

// The panels divide acquired by max connections, so both have to be there
// and max has to be the configured size.
func TestPoolStatsReportOccupancyAndWaits(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(previous) })

	// A pool connects lazily, so this opens no connection.
	pool, err := pgxpool.New(context.Background(), "postgres://memoh@127.0.0.1:1/memoh?pool_max_conns=7")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	telemetry.RecordPoolStats(pool)

	names := metricNames(t, reader)
	maxConns, ok := names["pgxpool.max_connections"]
	if !ok {
		t.Fatalf("pgxpool.max_connections not recorded; got %v", keys(names))
	}
	gauge, ok := maxConns.Data.(metricdata.Gauge[int64])
	if !ok || len(gauge.DataPoints) != 1 || gauge.DataPoints[0].Value != 7 {
		t.Errorf("pgxpool.max_connections = %+v, want one point of 7", maxConns.Data)
	}
	for _, want := range []string{"pgxpool.acquired_connections", "pgxpool.empty_acquire"} {
		if _, ok := names[want]; !ok {
			t.Errorf("%s not recorded", want)
		}
	}
}

func keys(m map[string]metricdata.Metrics) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
