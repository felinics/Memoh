package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type WSClientOptions struct {
	URL                  string
	Dialer               *websocket.Dialer
	Logger               *slog.Logger
	AckTimeout           time.Duration
	WriteTimeout         time.Duration
	ReadTimeout          time.Duration
	HeartbeatInterval    time.Duration
	ReconnectBaseDelay   time.Duration
	ReconnectMaxDelay    time.Duration
	MaxReconnectAttempts int
}

type WSClient struct {
	opts   WSClientOptions
	logger *slog.Logger

	writeMu sync.Mutex
	waitMu  sync.Mutex
	connMu  sync.RWMutex

	conn    *websocket.Conn
	waiters map[string]chan wsAck
	closed  bool
}

type wsAck struct {
	frame WSFrame
	err   error
}

func NewWSClient(opts WSClientOptions) *WSClient {
	if strings.TrimSpace(opts.URL) == "" {
		opts.URL = defaultWSURL
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.AckTimeout <= 0 {
		opts.AckTimeout = 8 * time.Second
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = 8 * time.Second
	}
	if opts.ReadTimeout <= 0 {
		opts.ReadTimeout = 70 * time.Second
	}
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = 30 * time.Second
	}
	if opts.ReconnectBaseDelay <= 0 {
		opts.ReconnectBaseDelay = 1 * time.Second
	}
	if opts.ReconnectMaxDelay <= 0 {
		opts.ReconnectMaxDelay = 30 * time.Second
	}
	return &WSClient{
		opts:    opts,
		logger:  opts.Logger.With(slog.String("component", "wecom_ws_client")),
		waiters: make(map[string]chan wsAck),
	}
}

func (c *WSClient) Run(ctx context.Context, auth AuthCredentials, onFrame func(context.Context, WSFrame) error) error {
	if err := auth.Validate(); err != nil {
		return err
	}
	attempt := 0
	for {
		err := c.runSession(ctx, auth, onFrame)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.isClosed() {
			return nil
		}
		if c.opts.MaxReconnectAttempts >= 0 && attempt >= c.opts.MaxReconnectAttempts {
			if err == nil {
				return errors.New("wecom websocket reconnect attempts exceeded")
			}
			return err
		}
		delay := c.backoff(attempt)
		attempt++
		c.logger.WarnContext(ctx, "wecom websocket session ended; reconnecting",
			slog.Int("attempt", attempt),
			slog.Duration("delay", delay),
			slog.Any("error", err),
		)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *WSClient) runSession(ctx context.Context, auth AuthCredentials, onFrame func(context.Context, WSFrame) error) error {
	conn, resp, err := c.dial(ctx)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return err
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	c.setConn(conn)
	sessionCtx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		c.clearConn()
		c.failAllWaiters(errors.New("wecom websocket disconnected"))
	}()

	readErrCh := make(chan error, 1)
	go c.readLoop(sessionCtx, onFrame, readErrCh)

	if err := c.authenticate(sessionCtx, auth); err != nil {
		_ = conn.Close()
		return err
	}
	c.logger.InfoContext(ctx, "wecom websocket authenticated")

	go c.heartbeatLoop(sessionCtx)

	select {
	case <-sessionCtx.Done():
		_ = conn.Close()
		return sessionCtx.Err()
	case err := <-readErrCh:
		_ = conn.Close()
		return err
	}
}

func (c *WSClient) dial(ctx context.Context) (*websocket.Conn, *http.Response, error) {
	dialer := c.opts.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}
	return dialer.DialContext(ctx, c.opts.URL, nil)
}

func (c *WSClient) authenticate(ctx context.Context, auth AuthCredentials) error {
	frame, err := BuildFrame(WSCmdSubscribe, NewReqID(WSCmdSubscribe), map[string]string{
		"bot_id": strings.TrimSpace(auth.BotID),
		"secret": strings.TrimSpace(auth.Credential),
	})
	if err != nil {
		return err
	}
	ack, err := c.SendWithAck(ctx, frame)
	if err != nil {
		return err
	}
	if ack.ErrCode != 0 {
		return fmt.Errorf("wecom subscribe failed: %s (code: %d)", strings.TrimSpace(ack.ErrMsg), ack.ErrCode)
	}
	return nil
}

func (c *WSClient) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(c.opts.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			frame, err := BuildFrame(WSCmdHeartbeat, NewReqID(WSCmdHeartbeat), nil)
			if err != nil {
				c.logger.ErrorContext(ctx, "build heartbeat frame failed", slog.Any("error", err))
				continue
			}
			err = c.Send(ctx, frame)
			if err != nil {
				c.logger.WarnContext(ctx, "wecom websocket heartbeat failed", slog.Any("error", err))
				if conn := c.getConn(); conn != nil {
					_ = conn.Close()
				}
				return
			}
		}
	}
}

func (c *WSClient) readLoop(ctx context.Context, onFrame func(context.Context, WSFrame) error, errCh chan<- error) {
	conn := c.getConn()
	if conn == nil {
		errCh <- errors.New("wecom websocket connection not ready")
		return
	}
	for {
		if c.opts.ReadTimeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(c.opts.ReadTimeout))
		}
		_, payload, err := conn.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		var frame WSFrame
		if err := json.Unmarshal(payload, &frame); err != nil {
			c.logger.WarnContext(ctx, "decode websocket frame failed", slog.Any("error", err))
			continue
		}
		if c.dispatchAck(frame) {
			continue
		}
		if onFrame == nil {
			continue
		}
		if err := onFrame(ctx, frame); err != nil {
			c.logger.WarnContext(ctx, "wecom onFrame callback returned error", slog.Any("error", err))
		}
	}
}

func (c *WSClient) Send(ctx context.Context, frame WSFrame) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if strings.TrimSpace(frame.Headers.ReqID) == "" {
		return errors.New("req_id is required")
	}
	conn := c.getConn()
	if conn == nil {
		return errors.New("wecom websocket is not connected")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.opts.WriteTimeout > 0 {
		_ = conn.SetWriteDeadline(time.Now().Add(c.opts.WriteTimeout))
	}
	if err := conn.WriteJSON(frame); err != nil {
		return err
	}
	return nil
}

func (c *WSClient) SendWithAck(ctx context.Context, frame WSFrame) (WSFrame, error) {
	reqID := strings.TrimSpace(frame.Headers.ReqID)
	if reqID == "" {
		return WSFrame{}, errors.New("req_id is required")
	}
	wait := make(chan wsAck, 1)
	c.waitMu.Lock()
	c.waiters[reqID] = wait
	c.waitMu.Unlock()
	defer func() {
		c.waitMu.Lock()
		delete(c.waiters, reqID)
		c.waitMu.Unlock()
	}()
	if err := c.Send(ctx, frame); err != nil {
		return WSFrame{}, err
	}
	timeout := c.opts.AckTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return WSFrame{}, ctx.Err()
	case <-timer.C:
		return WSFrame{}, fmt.Errorf("wait websocket ack timeout for req_id=%s", reqID)
	case ack := <-wait:
		if ack.err != nil {
			return WSFrame{}, ack.err
		}
		return ack.frame, nil
	}
}

func (c *WSClient) Close() error {
	c.connMu.Lock()
	c.closed = true
	conn := c.conn
	c.conn = nil
	c.connMu.Unlock()
	c.failAllWaiters(errors.New("wecom websocket client closed"))
	if conn == nil {
		return nil
	}
	return conn.Close()
}

func (c *WSClient) Reply(ctx context.Context, reqID string, cmd string, body any) (WSFrame, error) {
	frame, err := BuildFrame(cmd, reqID, body)
	if err != nil {
		return WSFrame{}, err
	}
	// WeCom callback reply commands are triggered by inbound callback req_id and may
	// not always return an explicit ACK frame in production. Waiting for ACK here can
	// cause false timeouts even when the platform accepts the reply.
	if isRespondCommand(cmd) {
		if err := c.Send(ctx, frame); err != nil {
			return WSFrame{}, err
		}
		return WSFrame{}, nil
	}
	return c.SendWithAck(ctx, frame)
}

func isRespondCommand(cmd string) bool {
	switch strings.TrimSpace(cmd) {
	case WSCmdRespond, WSCmdRespondWelcome, WSCmdRespondUpdate:
		return true
	default:
		return false
	}
}

func (c *WSClient) dispatchAck(frame WSFrame) bool {
	reqID := strings.TrimSpace(frame.Headers.ReqID)
	if reqID == "" {
		return false
	}
	c.waitMu.Lock()
	wait, ok := c.waiters[reqID]
	c.waitMu.Unlock()
	if !ok {
		return false
	}
	select {
	case wait <- wsAck{frame: frame}:
	default:
	}
	return true
}

func (c *WSClient) failAllWaiters(cause error) {
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	for id, wait := range c.waiters {
		delete(c.waiters, id)
		select {
		case wait <- wsAck{err: cause}:
		default:
		}
	}
}

func (c *WSClient) backoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := c.opts.ReconnectBaseDelay << attempt
	if delay > c.opts.ReconnectMaxDelay {
		return c.opts.ReconnectMaxDelay
	}
	return delay
}

func (c *WSClient) setConn(conn *websocket.Conn) {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	c.conn = conn
}

func (c *WSClient) clearConn() {
	c.connMu.Lock()
	conn := c.conn
	c.conn = nil
	c.connMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (c *WSClient) getConn() *websocket.Conn {
	c.connMu.RLock()
	defer c.connMu.RUnlock()
	return c.conn
}

func (c *WSClient) isClosed() bool {
	c.connMu.RLock()
	defer c.connMu.RUnlock()
	return c.closed
}
