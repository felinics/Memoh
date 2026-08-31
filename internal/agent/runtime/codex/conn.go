package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
)

// maxLineBytes bounds one NDJSON line from the app-server. Turn payloads can
// carry whole file diffs; 32MiB is far above anything observed and still a
// hard stop against a runaway stream.
const maxLineBytes = 32 * 1024 * 1024

// ErrConnClosed reports that the app-server connection is gone.
var ErrConnClosed = errors.New("codex app-server connection closed")

// inboundHandler receives server → client requests and notifications. Calls
// are serialized by the read loop; implementations must not block on it —
// long work (approvals) must move to its own goroutine.
type inboundHandler interface {
	HandleServerRequest(ctx context.Context, req *protocol.Inbound)
	HandleNotification(ctx context.Context, note *protocol.Inbound)
}

// procIO is the stdio surface conn drives; appServerProcess implements it,
// and tests substitute an in-memory pipe.
type procIO interface {
	io.ReadWriter
	Close() error
}

// conn multiplexes JSON-RPC over one app-server process: correlated request /
// response pairs out, server requests and notifications in.
type conn struct {
	proc    procIO
	logger  *slog.Logger
	handler inboundHandler

	writeMu sync.Mutex
	nextID  atomic.Uint64

	mu      sync.Mutex
	pending map[string]chan *protocol.Inbound
	closed  bool
	err     error
}

//nolint:contextcheck // the read loop outlives any caller context by design
func newConn(proc procIO, handler inboundHandler, logger *slog.Logger) *conn {
	c := &conn{
		proc:    proc,
		logger:  logger,
		handler: handler,
		pending: map[string]chan *protocol.Inbound{},
	}
	go c.readLoop()
	return c
}

// Call sends a request and decodes the response result into result (skipped
// when result is nil). It returns *protocol.RPCError for server-side errors.
func (c *conn) Call(ctx context.Context, method string, params any, result any) error {
	id := protocol.NewRequestID(c.nextID.Add(1))
	respCh := make(chan *protocol.Inbound, 1)
	if err := c.registerPending(id.Key(), respCh); err != nil {
		return err
	}
	if err := c.writeLine(protocol.Request{ID: id, Method: method, Params: params}); err != nil {
		c.unregisterPending(id.Key())
		return err
	}
	select {
	case <-ctx.Done():
		c.unregisterPending(id.Key())
		return ctx.Err()
	case resp, ok := <-respCh:
		if !ok {
			return c.closeErr()
		}
		if resp.Err != nil {
			return resp.Err
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("codex: decoding %s response: %w", method, err)
		}
		return nil
	}
}

// Notify sends a fire-and-forget notification.
func (c *conn) Notify(method string, params any) error {
	return c.writeLine(protocol.Notification{Method: method, Params: params})
}

// Respond answers a server → client request.
func (c *conn) Respond(id protocol.RequestID, result any) error {
	return c.writeLine(protocol.Response{ID: id, Result: result})
}

// RespondError rejects a server → client request.
func (c *conn) RespondError(id protocol.RequestID, code int64, message string) error {
	return c.writeLine(protocol.ErrorResponse{ID: id, Error: &protocol.RPCError{Code: code, Message: message}})
}

// writeTimeout bounds one stdio write. A bridge stream that cannot accept a
// small line within it is wedged; the connection (and process) come down
// rather than chaining every caller behind the write mutex forever.
const writeTimeout = 30 * time.Second

func (c *conn) writeLine(payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, writeErr := c.proc.Write(append(encoded, '\n'))
		done <- writeErr
	}()
	select {
	case writeErr := <-done:
		if writeErr != nil {
			return errors.Join(ErrConnClosed, writeErr)
		}
		return nil
	case <-time.After(writeTimeout):
		c.shutdown(errors.New("app-server stdio write stalled"))
		return c.closeErr()
	}
}

func (c *conn) registerPending(key string, ch chan *protocol.Inbound) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.errLocked()
	}
	c.pending[key] = ch
	return nil
}

func (c *conn) unregisterPending(key string) {
	c.mu.Lock()
	delete(c.pending, key)
	c.mu.Unlock()
}

func (c *conn) closeErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.errLocked()
}

func (c *conn) errLocked() error {
	if c.err != nil {
		return errors.Join(ErrConnClosed, c.err)
	}
	return ErrConnClosed
}

func (c *conn) readLoop() {
	scanner := bufio.NewScanner(c.proc)
	scanner.Buffer(make([]byte, 64*1024), maxLineBytes)
	ctx := context.Background()
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		inbound, err := protocol.DecodeInbound(line)
		if err != nil {
			// Non-JSON output on stdout is unexpected but survivable.
			c.logger.Warn("codex: undecodable app-server line", slog.String("line", truncateForLog(line)), slog.Any("error", err))
			continue
		}
		switch inbound.Kind {
		case protocol.InboundResponse:
			if inbound.ID.IsZero() {
				c.logger.Warn("codex: orphan null-id response", slog.String("line", truncateForLog(line)))
				continue
			}
			c.mu.Lock()
			ch, ok := c.pending[inbound.ID.Key()]
			if ok {
				delete(c.pending, inbound.ID.Key())
			}
			c.mu.Unlock()
			if !ok {
				c.logger.Warn("codex: response for unknown request id", slog.String("id", inbound.ID.String()))
				continue
			}
			ch <- inbound
		case protocol.InboundRequest:
			c.handler.HandleServerRequest(ctx, inbound)
		case protocol.InboundNotification:
			c.handler.HandleNotification(ctx, inbound)
		}
	}
	scanErr := scanner.Err()
	if scanErr == nil {
		scanErr = io.EOF
	}
	c.shutdown(scanErr)
}

// shutdown fails all pending calls and marks the connection closed. The
// process goes down with the connection: a server nobody can talk to is a
// zombie that would otherwise pass liveness checks forever.
func (c *conn) shutdown(cause error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if !errors.Is(cause, io.EOF) {
		c.err = cause
	}
	pending := c.pending
	c.pending = map[string]chan *protocol.Inbound{}
	c.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
	_ = c.proc.Close()
}

// Close tears the process down and fails pending calls.
func (c *conn) Close() error {
	err := c.proc.Close()
	c.shutdown(io.EOF)
	return err
}

func truncateForLog(line []byte) string {
	const limit = 512
	if len(line) <= limit {
		return string(line)
	}
	return string(line[:limit]) + "…"
}
