package telemetry

import (
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/felinics/memoh/internal/httpx"
)

// livenessRoute is the path both processes answer probes on.
const livenessRoute = "/health"

// EchoServer traces inbound HTTP requests and records their duration as
// http.server.request.duration.
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
//
// The span and the metric cover the same requests, with the same exceptions
// below. The metric carries fewer attributes: every attribute value is a
// separate series for as long as the process runs, so it keeps only what is
// bounded. The concrete path, the request id and the client address are per
// request; server.address is whatever Host the caller sent.
//
// A WebSocket is skipped, and has to be. Echo calls the handler and the
// handler does not return until the socket closes, which for a chat
// connection is hours. A span around that measures how long someone left a
// tab open, stays open for its whole duration, and — because it is the
// current span while it is open — adopts every turn sent over the connection
// into one trace. Handlers that upgrade trace the handshake themselves, which
// is the part with a duration worth having.
func EchoServer(next echo.HandlerFunc) echo.HandlerFunc {
	tracer := otel.Tracer(ScopeName)
	propagator := otel.GetTextMapPropagator()
	duration, err := otel.Meter(ScopeName).Float64Histogram(semconv.HTTPServerRequestDurationName,
		metric.WithUnit(semconv.HTTPServerRequestDurationUnit),
		metric.WithDescription(semconv.HTTPServerRequestDurationDescription),
	)
	if err != nil {
		// The API still returns a usable no-op instrument alongside the error.
		otel.Handle(err)
	}

	return func(c echo.Context) error {
		req := c.Request()
		if websocket.IsWebSocketUpgrade(req) {
			return next(c)
		}
		// The liveness probe is skipped too, for a duller reason: it runs
		// every few seconds forever, and its span says the same thing every
		// time. Left in, it is most of what a trace backend stores and most
		// of what it bills for — on a dev stack idling overnight it was the
		// only thing in there.
		//
		// /ping is not skipped even though it looks like a sibling. It
		// reports the server's capabilities and the desktop app calls it to
		// test a connection, so a slow or failing one is a real answer to a
		// real question.
		if c.Path() == livenessRoute {
			return next(c)
		}
		start := time.Now()
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
		if err != nil {
			span.RecordError(err)
			// Until the error handler has run, the response status is the
			// default 200 for anything a handler returned rather than wrote,
			// and only the handler knows what the error becomes: an
			// apperror maps to its own status and anything else to 500.
			// Running it here is what RequestLogger's HandleError does; the
			// router's call on the way out then finds the response committed
			// and does nothing.
			if !c.Response().Committed {
				c.Error(err)
			}
		}

		status := c.Response().Status
		span.SetAttributes(semconv.HTTPResponseStatusCode(status))
		// 4xx is the caller's problem, not this service failing, and marking
		// it an error makes every backend's error rate track ordinary traffic
		// like a bad password. Same rule as the WARN/ERROR split in the logs.
		if status >= 500 {
			span.SetStatus(codes.Error, "")
		}
		streaming := isEventStream(c.Response().Header())
		if streaming {
			span.SetAttributes(httpResponseStreaming)
		}
		duration.Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributeSet(echoMetricAttrs(c, status, streaming)))
		return err
	}
}

// httpResponseStreaming marks a Server-Sent Events response. The HTTP
// conventions have no attribute for it, so the name is our own. An event
// stream's duration is how long someone kept the page
// open, not how long they waited, so latency panels leave these out while
// request and error counts keep them.
var httpResponseStreaming = attribute.Bool("http.response.streaming", true)

// isEventStream decides from the response alone. The web client subscribes
// without sending Accept: text/event-stream, so the request says nothing.
func isEventStream(header http.Header) bool {
	mediaType, _, err := mime.ParseMediaType(header.Get(echo.HeaderContentType))
	return err == nil && mediaType == "text/event-stream"
}

func echoMetricAttrs(c echo.Context, status int, streaming bool) attribute.Set {
	req := c.Request()
	scheme := "http"
	if req.TLS != nil {
		scheme = "https"
	}
	attrs := []attribute.KeyValue{
		semconv.HTTPRequestMethodKey.String(boundedMethod(req.Method)),
		semconv.URLScheme(scheme),
		semconv.HTTPResponseStatusCode(status),
		semconv.NetworkProtocolVersion(httpProtocolVersion(req.Proto)),
	}
	if route := c.Path(); route != "" {
		attrs = append(attrs, semconv.HTTPRoute(route))
	}
	if streaming {
		attrs = append(attrs, httpResponseStreaming)
	}
	return attribute.NewSet(attrs...)
}

// boundedMethod folds a method outside the registered set into _OTHER, as the
// HTTP conventions require of metrics: the method is whatever the caller
// sent, and each distinct value would otherwise be a series of its own.
func boundedMethod(method string) string {
	switch method = strings.ToUpper(method); method {
	case http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodOptions,
		http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace:
		return method
	default:
		return "_OTHER"
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
