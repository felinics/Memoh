package handlers

import (
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsHeartbeat keeps an idle WebSocket open through proxies and notices a peer
// that has gone away without closing.
//
// A load balancer or reverse proxy drops a connection that carries nothing
// for its idle timeout, which for a chat tab left open between turns is most
// of the time. A ping is traffic, so it keeps the path open. It also turns a
// vanished peer, such as a laptop that went to sleep, into a read timeout:
// without one the read below waits forever, and the connection's
// subscriptions with it.
//
// Browsers answer pings on their own, so nothing changes for the client.
type wsHeartbeat struct {
	// pingInterval is how often a ping is sent. It has to be well under the
	// shortest idle timeout on the path.
	pingInterval time.Duration
	// readTimeout is how long the connection may go without a pong or a
	// message before it is treated as dead. It covers more than one ping, so
	// a single lost pong does not end the connection.
	readTimeout time.Duration
	// writeTimeout bounds a single ping. A peer that has stopped reading
	// fills the socket buffer, and a ping without a deadline would wait on
	// it forever.
	writeTimeout time.Duration
}

var chatWSHeartbeat = wsHeartbeat{
	pingInterval: 20 * time.Second,
	readTimeout:  60 * time.Second,
	writeTimeout: 10 * time.Second,
}

// wsKeepalive is one connection's heartbeat.
type wsKeepalive struct {
	conn        *websocket.Conn
	readTimeout time.Duration
	stopOnce    sync.Once
	stop        chan struct{}
	done        chan struct{}
}

// start begins pinging. The caller owns the read loop: it calls extend
// before every read, which also arms the first deadline, and Stop when the
// connection is finished. Stop does not return until the ping goroutine has
// exited.
func (hb wsHeartbeat) start(conn *websocket.Conn) *wsKeepalive {
	k := &wsKeepalive{
		conn:        conn,
		readTimeout: hb.readTimeout,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	// The pong handler runs inside the caller's read, so it needs no lock.
	conn.SetPongHandler(func(string) error {
		k.extend()
		return nil
	})
	go k.ping(hb.pingInterval, hb.writeTimeout)
	return k
}

func (k *wsKeepalive) ping(interval, writeTimeout time.Duration) {
	defer close(k.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-k.stop:
			return
		case <-ticker.C:
			// WriteControl may run alongside the connection's other writer;
			// gorilla/websocket allows that for control frames. A failed ping
			// means the connection is broken, and the read deadline ends the
			// read loop, so there is nothing further to do here.
			if err := k.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				return
			}
		}
	}
}

// extend pushes the read deadline back. Every message counts as a sign of
// life, not only pongs.
func (k *wsKeepalive) extend() {
	_ = k.conn.SetReadDeadline(time.Now().Add(k.readTimeout))
}

// Stop ends the ping goroutine and waits for it.
func (k *wsKeepalive) Stop() {
	k.stopOnce.Do(func() { close(k.stop) })
	<-k.done
}

// wsDisconnectAttrs says why a read loop ended, in values that can be counted.
// The error text is not logged: a close frame's reason is whatever the peer
// chose to send.
func wsDisconnectAttrs(err error) []any {
	var closeErr *websocket.CloseError
	switch {
	case errors.As(err, &closeErr):
		return []any{slog.String("reason", "closed"), slog.Int("close_code", closeErr.Code)}
	case errors.Is(err, os.ErrDeadlineExceeded):
		return []any{slog.String("reason", "heartbeat_timeout")}
	default:
		return []any{slog.String("reason", "read_error")}
	}
}
