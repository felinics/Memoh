// Package httpx holds tiny echo-boundary helpers shared by both HTTP shells
// and by handlers. Keep it dependency-light — echo, plus internal/logger,
// which imports no package from this repository and so cannot close a cycle.
package httpx

import (
	"net/url"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/logger"
)

// RequestID returns the request id assigned by the RequestID middleware
// (response header), falling back to a client-provided header. Empty when
// neither exists — callers should treat it as optional metadata.
func RequestID(c echo.Context) string {
	if c == nil {
		return ""
	}
	if id := c.Response().Header().Get(echo.HeaderXRequestID); id != "" {
		return id
	}
	return c.Request().Header.Get(echo.HeaderXRequestID)
}

// RequestIDContext carries the request id into the request's context.Context,
// so that anything logging during the request reports it.
//
// Without this the id exists only on the echo.Context and the response header.
// It reaches the client — error responses carry it as request_id — and it
// reaches the access log line, but nothing a handler or a service logs while
// serving the request can include it. An id a user quotes back therefore names
// a request whose actual work cannot be found.
//
// Install it directly after middleware.RequestID, which is what assigns the id.
func RequestIDContext(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if id := RequestID(c); id != "" {
			r := c.Request()
			c.SetRequest(r.WithContext(logger.ContextWithRequestID(r.Context(), id)))
		}
		return next(c)
	}
}

// SafeRequestLogURI renders a request URI for an access log line: the path,
// never the query string.
//
// Some URLs authorise with a token in the query string, so a log line that
// keeps the query publishes credentials to anything that reads logs. Dropping
// it for every request rather than for the paths known to carry one means a
// new such path cannot arrive unnoticed.
//
// This lives here rather than next to one server's middleware because both
// HTTP shells need it. A sanitizer that only one of them uses is the worst of
// the three possible states: the tokens still reach a log, and the shell that
// redacts them suggests otherwise.
func SafeRequestLogURI(u *url.URL, fallback string) string {
	if u == nil {
		var err error
		u, err = url.ParseRequestURI(fallback)
		if err != nil {
			return ""
		}
	}
	return u.EscapedPath()
}
