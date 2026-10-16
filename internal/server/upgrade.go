package server

import (
	"bufio"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

// recordUpgradeStatus keeps echo's view of the response true for a WebSocket
// handshake, so the request log says 101 for an upgraded connection and the
// real status for a refused one.
//
// Neither WebSocket library tells echo what happened. Both take the
// connection over with Hijack and write the 101 on the raw connection, which
// leaves the response at echo's default 200. coder/websocket is also handed
// the raw writer rather than echo's, so a handshake it rejects is written
// past echo as well: the log said 200, and the error handler, finding the
// response uncommitted, wrote a second one. Wrapping the writer covers both,
// because both reach the connection through it.
func recordUpgradeStatus(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if websocket.IsWebSocketUpgrade(c.Request()) {
			res := c.Response()
			res.Writer = &upgradeWriter{ResponseWriter: res.Writer, res: res}
		}
		return next(c)
	}
}

type upgradeWriter struct {
	http.ResponseWriter
	res *echo.Response
}

func (w *upgradeWriter) WriteHeader(code int) {
	w.res.Status = code
	w.res.Committed = true
	w.ResponseWriter.WriteHeader(code)
}

// Hijack only records the switch once it has succeeded: a handshake that
// fails to take the connection over still has an ordinary response to write.
func (w *upgradeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.res.Status = http.StatusSwitchingProtocols
		w.res.Committed = true
	}
	return conn, rw, err
}

// Unwrap lets http.ResponseController reach the server's writer for the
// methods not overridden here.
func (w *upgradeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
