package telemetry_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/felinics/memoh/internal/httpx"
	"github.com/felinics/memoh/internal/telemetry"
)

// recordSpans points the global provider at an in-memory exporter for the
// duration of one test. The middleware reads the global provider, which is
// how it works in the process, so the test has to install one too.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	return recorder
}

func serve(t *testing.T, req *http.Request, handler echo.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	e.Use(middleware.RequestID())
	e.Use(httpx.RequestIDContext)
	e.Use(telemetry.EchoServer)
	e.GET("/bots/:id", handler)
	// The routes the skip rules are about. Registered here rather than in
	// each test because the middleware reads the matched route, so an
	// unregistered path would exercise the 404 path instead of the rule.
	e.HEAD("/health", handler)
	e.GET("/health", handler)
	e.GET("/ping", handler)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func attrs(span sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	out := map[attribute.Key]attribute.Value{}
	for _, kv := range span.Attributes() {
		out[kv.Key] = kv.Value
	}
	return out
}

func TestEchoServerNamesTheSpanAfterTheRouteNotThePath(t *testing.T) {
	// "GET /bots/:id" groups every bot's requests into one operation. Naming
	// the span after the concrete path makes each bot its own operation,
	// which no backend can aggregate and every backend has to index.
	recorder := recordSpans(t)
	serve(t, httptest.NewRequest(http.MethodGet, "/bots/bot-1", nil), func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("want one span, got %d", len(spans))
	}
	if got := spans[0].Name(); got != "GET /bots/:id" {
		t.Errorf("span name = %q, want the matched route", got)
	}
	if got := spans[0].SpanKind(); got != trace.SpanKindServer {
		t.Errorf("span kind = %v, want server", got)
	}
}

func TestEchoServerContinuesAnIncomingTrace(t *testing.T) {
	// The reason propagation exists: a request that already belongs to a
	// trace must extend it, not start a second one that nothing links to.
	recorder := recordSpans(t)
	req := httptest.NewRequest(http.MethodGet, "/bots/bot-1", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	serve(t, req, func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("want one span, got %d", len(spans))
	}
	if got := spans[0].SpanContext().TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace id = %q, want the caller's", got)
	}
	if got := spans[0].Parent().SpanID().String(); got != "00f067aa0ba902b7" {
		t.Errorf("parent span id = %q, want the caller's", got)
	}
}

func TestEchoServerPutsTheRequestIDOnTheSpan(t *testing.T) {
	// The identifier a user quotes has to reach the trace backend, or a
	// support report can be found in logs and nowhere else.
	recorder := recordSpans(t)
	rec := serve(t, httptest.NewRequest(http.MethodGet, "/bots/bot-1", nil), func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	header := rec.Header().Get(echo.HeaderXRequestID)
	if header == "" {
		t.Fatal("no request id on the response")
	}
	got := attrs(recorder.Ended()[0])["request_id"].AsString()
	if got != header {
		t.Errorf("span request_id = %q, response header = %q", got, header)
	}
}

func TestEchoServerKeepsTheQueryOutOfTheSpan(t *testing.T) {
	// Span attributes are subject to the same rule as log records: a query
	// string can carry an authorising token.
	recorder := recordSpans(t)
	serve(t, httptest.NewRequest(http.MethodGet, "/bots/bot-1?token=synthetic-secret", nil), func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	for key, value := range attrs(recorder.Ended()[0]) {
		if str := value.AsString(); strings.Contains(str, "synthetic-secret") {
			t.Errorf("attribute %s leaked the query: %q", key, str)
		}
	}
	if got := attrs(recorder.Ended()[0])["url.path"].AsString(); got != "/bots/bot-1" {
		t.Errorf("url.path = %q, want the path without the query", got)
	}
}

func TestEchoServerMarksServerFailuresButNotRejections(t *testing.T) {
	// A 4xx is the caller's problem. Recording it as a span error makes every
	// backend's error rate track ordinary traffic — a wrong password, a stale
	// link — and the signal stops meaning anything.
	for _, tc := range []struct {
		name      string
		status    int
		wantError bool
	}{
		{"rejected", http.StatusUnauthorized, false},
		{"failed", http.StatusInternalServerError, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := recordSpans(t)
			serve(t, httptest.NewRequest(http.MethodGet, "/bots/bot-1", nil), func(echo.Context) error {
				return echo.NewHTTPError(tc.status)
			})

			span := recorder.Ended()[0]
			if got := span.Status().Code == codes.Error; got != tc.wantError {
				t.Errorf("span error = %v, want %v (status %d)", got, tc.wantError, tc.status)
			}
			if got := attrs(span)["http.response.status_code"].AsInt64(); got != int64(tc.status) {
				t.Errorf("status attribute = %d, want %d", got, tc.status)
			}
		})
	}
}

// A liveness probe runs every few seconds for the life of the process and
// its span says the same thing every time. Tracing it means the backend
// mostly stores probes.
func TestEchoServerDoesNotSpanTheLivenessProbe(t *testing.T) {
	recorder := recordSpans(t)

	for _, method := range []string{http.MethodHead, http.MethodGet} {
		t.Run(method, func(t *testing.T) {
			rec := serve(t, httptest.NewRequestWithContext(t.Context(), method, "/health", nil),
				func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
			}
		})
	}

	for _, span := range recorder.Ended() {
		if strings.Contains(span.Name(), "/health") {
			t.Errorf("probe produced a span named %q", span.Name())
		}
	}
}

// /ping looks like a sibling of /health and is not one. It reports the
// server's capabilities and the desktop app calls it to decide whether a
// server is usable, so a slow one is worth seeing.
func TestEchoServerStillSpansPing(t *testing.T) {
	recorder := recordSpans(t)

	rec := serve(t, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ping", nil),
		func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}

	var names []string
	for _, span := range recorder.Ended() {
		if span.Name() == "GET /ping" {
			return
		}
		names = append(names, span.Name())
	}
	t.Fatalf("no span for /ping; got %v", names)
}

// recordMetrics points the global meter provider at a manual reader for the
// duration of one test. The middleware takes its instrument from the global
// provider when it is built, and serve builds a fresh one per request.
func recordMetrics(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(previous) })
	return reader
}

// durationPoints returns the http.server.request.duration points collected
// so far, one per distinct attribute set.
func durationPoints(t *testing.T, reader *sdkmetric.ManualReader) []metricdata.HistogramDataPoint[float64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var points []metricdata.HistogramDataPoint[float64]
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "http.server.request.duration" {
				continue
			}
			if m.Unit != "s" {
				t.Errorf("unit = %q, want s", m.Unit)
			}
			histogram, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("http.server.request.duration is a %T, want a float64 histogram", m.Data)
			}
			points = append(points, histogram.DataPoints...)
		}
	}
	return points
}

func onlyPoint(t *testing.T, reader *sdkmetric.ManualReader) map[attribute.Key]attribute.Value {
	t.Helper()
	points := durationPoints(t, reader)
	if len(points) != 1 {
		t.Fatalf("want one duration point, got %d", len(points))
	}
	if points[0].Count != 1 {
		t.Errorf("count = %d, want 1", points[0].Count)
	}
	out := map[attribute.Key]attribute.Value{}
	for _, kv := range points[0].Attributes.ToSlice() {
		out[kv.Key] = kv.Value
	}
	return out
}

func TestEchoServerRecordsDurationByRouteNotPath(t *testing.T) {
	// The route is what makes the metric answer "which endpoint is slow". The
	// concrete path would make every bot a series of its own, and the other
	// per-request values would do the same.
	reader := recordMetrics(t)
	req := httptest.NewRequest(http.MethodGet, "/bots/bot-1?token=synthetic-secret", nil)
	req.Host = "attacker-chosen.example"
	serve(t, req, func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })

	got := onlyPoint(t, reader)
	want := map[attribute.Key]string{
		"http.request.method":      "GET",
		"url.scheme":               "http",
		"http.route":               "/bots/:id",
		"network.protocol.version": "1.1",
	}
	for key, value := range want {
		if got[key].AsString() != value {
			t.Errorf("%s = %q, want %q", key, got[key].Emit(), value)
		}
	}
	if code := got["http.response.status_code"].AsInt64(); code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", code, http.StatusNoContent)
	}
	for key := range got {
		if _, expected := want[key]; !expected && key != "http.response.status_code" {
			t.Errorf("unexpected metric attribute %s = %q", key, got[key].Emit())
		}
	}
}

func TestEchoServerFoldsUnknownMethods(t *testing.T) {
	// The method is whatever the caller sent; left as is, each spelling is a
	// series.
	reader := recordMetrics(t)
	serve(t, httptest.NewRequest("PURGE", "/bots/bot-1", nil), func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	if got := onlyPoint(t, reader)["http.request.method"].AsString(); got != "_OTHER" {
		t.Errorf("method = %q, want _OTHER", got)
	}
}

func TestEchoServerRecordsTheStatusTheClientReceived(t *testing.T) {
	// A returned error has not become a status yet when the handler returns.
	// Reading the response then gives echo's default 200 for every failure,
	// which is the one thing a failure dashboard must not show.
	for _, tc := range []struct {
		name    string
		path    string
		handler echo.HandlerFunc
		want    int
	}{
		{"unknown route", "/nowhere", nil, http.StatusNotFound},
		{"http error", "/bots/bot-1", func(echo.Context) error { return echo.NewHTTPError(http.StatusConflict) }, http.StatusConflict},
		{"plain error", "/bots/bot-1", func(echo.Context) error { return errors.New("synthetic failure") }, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := recordMetrics(t)
			recorder := recordSpans(t)
			handler := tc.handler
			if handler == nil {
				handler = func(c echo.Context) error { return c.NoContent(http.StatusNoContent) }
			}
			rec := serve(t, httptest.NewRequest(http.MethodGet, tc.path, nil), handler)
			if rec.Code != tc.want {
				t.Fatalf("response status = %d, want %d", rec.Code, tc.want)
			}

			if got := onlyPoint(t, reader)["http.response.status_code"].AsInt64(); got != int64(tc.want) {
				t.Errorf("metric status = %d, want %d", got, tc.want)
			}
			span := recorder.Ended()[0]
			if got := attrs(span)["http.response.status_code"].AsInt64(); got != int64(tc.want) {
				t.Errorf("span status = %d, want %d", got, tc.want)
			}
			if got, want := span.Status().Code == codes.Error, tc.want >= 500; got != want {
				t.Errorf("span error = %v, want %v", got, want)
			}
		})
	}
}

func TestEchoServerRecordsNoDurationForUpgradesOrProbes(t *testing.T) {
	// The same exceptions as the span. A WebSocket's duration is the life of
	// the connection, and a probe's is the same number every few seconds.
	reader := recordMetrics(t)
	upgrade := httptest.NewRequest(http.MethodGet, "/bots/bot-1", nil)
	upgrade.Header.Set("Connection", "Upgrade")
	upgrade.Header.Set("Upgrade", "websocket")
	serve(t, upgrade, func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })
	serve(t, httptest.NewRequest(http.MethodHead, "/health", nil), func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	if points := durationPoints(t, reader); len(points) != 0 {
		t.Fatalf("want no duration points, got %d", len(points))
	}
}

func TestEchoServerLabelsEventStreams(t *testing.T) {
	// An event stream lasts as long as the page that opened it, so latency
	// panels have to be able to leave it out. The response header decides:
	// the web client does not send Accept: text/event-stream.
	for _, tc := range []struct {
		name        string
		contentType string
		want        bool
	}{
		{"event stream", "Text/Event-Stream; charset=utf-8", true},
		{"json", "application/json", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := recordMetrics(t)
			recorder := recordSpans(t)
			serve(t, httptest.NewRequest(http.MethodGet, "/bots/bot-1", nil), func(c echo.Context) error {
				c.Response().Header().Set(echo.HeaderContentType, tc.contentType)
				c.Response().WriteHeader(http.StatusOK)
				_, err := c.Response().Write([]byte("data: {}\n\n"))
				return err
			})

			value, labeled := onlyPoint(t, reader)["http.response.streaming"]
			if labeled != tc.want || (labeled && !value.AsBool()) {
				t.Errorf("metric streaming = %v (present %v), want %v", value.Emit(), labeled, tc.want)
			}
			value, labeled = attrs(recorder.Ended()[0])["http.response.streaming"]
			if labeled != tc.want || (labeled && !value.AsBool()) {
				t.Errorf("span streaming = %v (present %v), want %v", value.Emit(), labeled, tc.want)
			}
		})
	}
}
