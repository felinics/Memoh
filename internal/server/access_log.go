package server

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/httpx"
)

// errUnrecorded stands in for the error of a request answered with a server
// status that no error was returned for, such as a handler writing a 500
// itself. It has no recorded origin, so the result is counted as unlocated.
var errUnrecorded = errors.New("server error status without a returned error")

// AccessLog writes the one result record for each request: the access
// fields, and for a failed request the error fields of its attribution. It
// recovers a panic into an error with the stack of the panic, and answers an
// error that is still unanswered through the shell's HTTP error handler, so
// the record carries the status that was sent.
//
// Install it directly after telemetry.EchoServer, so the record is logged
// with the request's span and every middleware below it is covered.
func AccessLog(log *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := serveRecovered(next, c)
			switch {
			case err != nil && !c.Response().Committed:
				// The error handler answers it and hands back the error it
				// attributes under resultErrorKey.
				c.Error(err)
			case err != nil:
				// A handler that wrote its own body, such as an SSE error
				// frame, returns the error the body was rendered from. It is
				// not answered again, only attributed by the same rule.
				err = transportError(err)
			}
			req := c.Request()
			ctx := req.Context()
			status := c.Response().Status
			if answered, ok := c.Get(resultErrorKey).(error); ok {
				err = answered
			}
			if err == nil && status >= http.StatusInternalServerError {
				err = errUnrecorded
			}
			result := errlog.Finish(ctx, operationName(c), err, errlog.Options{})
			attrs := []slog.Attr{
				slog.String("method", req.Method),
				slog.String("uri", httpx.SafeRequestLogURI(req.URL, req.RequestURI)),
			}
			// The matched template groups records by endpoint; a request that
			// matched no route has none.
			if route := c.Path(); route != "" {
				attrs = append(attrs, slog.String("route", route))
			}
			attrs = append(attrs,
				slog.Int("status", status),
				slog.Duration("latency", time.Since(start)),
				slog.String("remote_ip", c.RealIP()),
			)
			attrs = append(attrs, result.Attrs()...)
			log.LogAttrs(ctx, result.Level, "request", attrs...)
			return nil
		}
	}
}

// serveRecovered runs the handler and turns a panic into its error. The
// panic that net/http uses to abort a response is left to net/http.
func serveRecovered(next echo.HandlerFunc, c echo.Context) (err error) {
	defer func() {
		if v := recover(); v != nil {
			if v == http.ErrAbortHandler { //nolint:errorlint // recover returns the value that was passed to panic
				panic(v)
			}
			err = errs.Recovered(v)
		}
	}()
	return next(c)
}

// operationName is the bounded name of the request for the unlocated-error
// count: the matched route, not the path.
func operationName(c echo.Context) string {
	if route := c.Path(); route != "" {
		return "http " + route
	}
	return "http"
}
