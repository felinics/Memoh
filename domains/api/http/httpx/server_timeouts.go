package httpx

import (
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

// Timeout baseline for every listener we expose. http.Server's zero values mean
// "no timeout at all", which lets a slow client pin a goroutine and a file
// descriptor forever.
//
// ReadHeaderTimeout is the actual defence against Slowloris and is kept tight.
// ReadTimeout and WriteTimeout are deliberately loose backstops, because
// handlers that stream (SSE) or move payloads of unbounded size (uploads and
// downloads) opt out per request through ClearReadDeadline/ClearWriteDeadline.
// Hijacked connections are unaffected: the WebSocket upgrader clears the
// deadlines the server installed before the handshake.
const (
	ReadHeaderTimeout = 10 * time.Second
	ReadTimeout       = 5 * time.Minute
	WriteTimeout      = 5 * time.Minute
	IdleTimeout       = 120 * time.Second
	MaxHeaderBytes    = 1 << 20
)

// HardenServer applies the timeout baseline to srv.
func HardenServer(srv *http.Server) {
	srv.ReadHeaderTimeout = ReadHeaderTimeout
	srv.ReadTimeout = ReadTimeout
	srv.WriteTimeout = WriteTimeout
	srv.IdleTimeout = IdleTimeout
	srv.MaxHeaderBytes = MaxHeaderBytes
}

// ClearWriteDeadline lifts WriteTimeout for the current response. Handlers whose
// response has no bounded duration must call it, otherwise the server cuts them
// off mid-flight.
func ClearWriteDeadline(c echo.Context) error {
	return ignoreUnsupported(http.NewResponseController(c.Response()).SetWriteDeadline(time.Time{}))
}

// ClearReadDeadline lifts ReadTimeout for the current request. Handlers that read
// a request body of unbounded size must call it before consuming the body.
func ClearReadDeadline(c echo.Context) error {
	return ignoreUnsupported(http.NewResponseController(c.Response()).SetReadDeadline(time.Time{}))
}

// A ResponseWriter that carries no deadline at all (httptest.ResponseRecorder in
// handler tests) has nothing to lift, which is not a failure. Real listeners are
// covered by TestClearWriteDeadlineOutlivesServerWriteTimeout.
func ignoreUnsupported(err error) error {
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}
