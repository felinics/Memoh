package telemetry

import (
	"errors"

	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/felinics/memoh/internal/httpx"
)

// EchoServer traces inbound HTTP requests.
//
// This is written here rather than taken from a library because the
// contributed Echo instrumentation is deprecated in favour of a replacement
// that has not been released; depending on either would mean depending on
// something on its way out or on something with no tagged version. What the
// middleware has to do is small and stable, and writing it keeps two details
// under our control that a generic one would get wrong for this codebase:
// the span name uses the matched route rather than the raw path, and the URL
// attribute goes through the same sanitizer as the access log.
//
// Install it after middleware.RequestID and httpx.RequestIDContext, so the
// span can carry the id the client is given.
func EchoServer(next echo.HandlerFunc) echo.HandlerFunc {
	tracer := otel.Tracer(ScopeName)
	propagator := otel.GetTextMapPropagator()

	return func(c echo.Context) error {
		req := c.Request()
		// Continue the caller's trace when there is one. A public entrance
		// will usually not have one; an internal caller will.
		ctx := propagator.Extract(req.Context(), propagation.HeaderCarrier(req.Header))

		ctx, span := tracer.Start(ctx, echoSpanName(c),
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(echoRequestAttrs(c)...),
		)
		defer span.End()

		c.SetRequest(req.WithContext(ctx))
		err := next(c)

		status := c.Response().Status
		if err != nil {
			// The error handler has not run yet, so the response status is
			// still the zero value for anything a handler returned rather
			// than wrote. echo.HTTPError carries the status it will become.
			var httpErr *echo.HTTPError
			if errors.As(err, &httpErr) {
				status = httpErr.Code
			}
			span.RecordError(err)
		}
		span.SetAttributes(semconv.HTTPResponseStatusCode(status))
		// 4xx is the caller's problem, not this service failing, and marking
		// it an error makes every backend's error rate track ordinary traffic
		// like a bad password. Same rule as the WARN/ERROR split in the logs.
		if status >= 500 {
			span.SetStatus(codes.Error, "")
		}
		return err
	}
}

// echoSpanName is the matched route, not the path: "/bots/:id" groups the
// requests for every bot into one operation, where "/bots/abc123" would make
// each bot its own, which is unreadable and expensive to index.
func echoSpanName(c echo.Context) string {
	method := c.Request().Method
	if route := c.Path(); route != "" {
		return method + " " + route
	}
	return method
}

func echoRequestAttrs(c echo.Context) []attribute.KeyValue {
	req := c.Request()
	attrs := []attribute.KeyValue{
		semconv.HTTPRequestMethodKey.String(req.Method),
		// The same sanitizer the access log uses: a query string can carry an
		// authorising token, and a span attribute is no safer a place for one
		// than a log line.
		semconv.URLPath(httpx.SafeRequestLogURI(req.URL, req.RequestURI)),
		semconv.NetworkProtocolVersion(httpProtocolVersion(req.Proto)),
	}
	if route := c.Path(); route != "" {
		attrs = append(attrs, semconv.HTTPRoute(route))
	}
	if host := req.Host; host != "" {
		attrs = append(attrs, semconv.ServerAddress(host))
	}
	if ip := c.RealIP(); ip != "" {
		attrs = append(attrs, semconv.ClientAddress(ip))
	}
	// The id the client was handed. Without it a support report quoting a
	// request id can be found in logs but not in a trace backend.
	if id := httpx.RequestID(c); id != "" {
		attrs = append(attrs, attribute.String("request_id", id))
	}
	return attrs
}

func httpProtocolVersion(proto string) string {
	switch proto {
	case "HTTP/1.0":
		return "1.0"
	case "HTTP/1.1":
		return "1.1"
	case "HTTP/2.0", "HTTP/2":
		return "2"
	default:
		return proto
	}
}
