// Package httpx holds tiny echo-boundary helpers shared by the server shell
// and HTTP handlers. Keep it dependency-light (echo only) so both sides can
// import it without cycles.
package httpx

import "github.com/labstack/echo/v4"

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
