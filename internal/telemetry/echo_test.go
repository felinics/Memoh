package telemetry_test

import (
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
