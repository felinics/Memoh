package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	sdkjsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"

	mcptools "github.com/memohai/memoh/domains/agent/mcp"
)

// fakeMCPConnection implements sdkmcp.Connection for testing.
// onWrite is called synchronously when Write is called; if it returns a
// non-nil Response the response is queued to be returned by Read.
type fakeMCPConnection struct {
	mu      sync.Mutex
	writes  []*sdkjsonrpc.Request
	readCh  chan sdkjsonrpc.Message
	closed  chan struct{}
	closeMu sync.Once
	onWrite func(req *sdkjsonrpc.Request) (*sdkjsonrpc.Response, error)
}

func newFakeMCPConnection(onWrite func(req *sdkjsonrpc.Request) (*sdkjsonrpc.Response, error)) *fakeMCPConnection {
	return &fakeMCPConnection{
		writes:  make([]*sdkjsonrpc.Request, 0, 16),
		readCh:  make(chan sdkjsonrpc.Message, 32),
		closed:  make(chan struct{}),
		onWrite: onWrite,
	}
}

func (c *fakeMCPConnection) Read(ctx context.Context) (sdkjsonrpc.Message, error) {
	select {
	case <-c.closed:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	case msg, ok := <-c.readCh:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	}
}

func (c *fakeMCPConnection) Write(ctx context.Context, msg sdkjsonrpc.Message) error {
	req, ok := msg.(*sdkjsonrpc.Request)
	if !ok {
		return fmt.Errorf("unsupported message type: %T", msg)
	}
	cloned := cloneJSONRPCRequest(req)
	c.mu.Lock()
	c.writes = append(c.writes, cloned)
	c.mu.Unlock()

	if c.onWrite == nil {
		return nil
	}
	resp, err := c.onWrite(cloned)
	if err != nil {
		return err
	}
	if resp == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return io.EOF
	case c.readCh <- resp:
		return nil
	}
}

func (c *fakeMCPConnection) Close() error {
	c.closeMu.Do(func() {
		close(c.closed)
		close(c.readCh)
	})
	return nil
}

func (*fakeMCPConnection) SessionID() string { return "test-session" }

func cloneJSONRPCRequest(req *sdkjsonrpc.Request) *sdkjsonrpc.Request {
	if req == nil {
		return nil
	}
	params := append([]byte(nil), req.Params...)
	return &sdkjsonrpc.Request{
		ID:     req.ID,
		Method: req.Method,
		Params: params,
		Extra:  req.Extra,
	}
}

func jsonRPCSuccessResponse(id sdkjsonrpc.ID, payload map[string]any) *sdkjsonrpc.Response {
	body, _ := json.Marshal(payload)
	return &sdkjsonrpc.Response{ID: id, Result: body}
}

func newTestMCPSession(conn *fakeMCPConnection) *mcpSession {
	readCtx, cancelRead := context.WithCancel(context.Background()) //nolint:gosec // G118: cancelRead is stored in mcpSession.cancelRead
	return &mcpSession{
		pending:    map[string]chan *sdkjsonrpc.Response{},
		conn:       conn,
		closed:     make(chan struct{}),
		readCtx:    readCtx,
		cancelRead: cancelRead,
	}
}

// --- Tests ---

// TestMCPSession_CallRaw_ResponseEnvelope verifies that callRaw returns a
// standard JSON-RPC envelope {"jsonrpc","id","result"}.
func TestMCPSession_CallRaw_ResponseEnvelope(t *testing.T) {
	conn := newFakeMCPConnection(func(req *sdkjsonrpc.Request) (*sdkjsonrpc.Response, error) {
		return jsonRPCSuccessResponse(req.ID, map[string]any{"tools": []any{}}), nil
	})
	sess := newTestMCPSession(conn)
	sess.initState = mcpSessionInitStateReady
	go sess.readLoop()
	defer sess.closeWithError(io.EOF)

	payload, err := sess.call(context.Background(), mcptools.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      mcptools.RawStringID("1"),
		Method:  "tools/list",
	})
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}

	// Verify standard JSON-RPC envelope.
	if payload["jsonrpc"] != "2.0" {
		t.Errorf("expected jsonrpc=2.0, got %v", payload["jsonrpc"])
	}
	if _, ok := payload["id"]; !ok {
		t.Errorf("expected 'id' field in envelope, got %v", payload)
	}
	if _, ok := payload["result"]; !ok {
		t.Errorf("expected 'result' field in envelope, got %v", payload)
	}
	if _, ok := payload["error"]; ok {
		t.Errorf("unexpected 'error' field in success envelope")
	}
}

// TestMCPSession_CallRaw_ErrorEnvelope verifies that server-side errors are
// returned as {"jsonrpc","id","error"} envelope, not a Go error.
func TestMCPSession_CallRaw_ErrorEnvelope(t *testing.T) {
	conn := newFakeMCPConnection(func(req *sdkjsonrpc.Request) (*sdkjsonrpc.Response, error) {
		return &sdkjsonrpc.Response{
			ID:    req.ID,
			Error: &sdkjsonrpc.Error{Code: -32601, Message: "Method not found"},
		}, nil
	})
	sess := newTestMCPSession(conn)
	sess.initState = mcpSessionInitStateReady
	go sess.readLoop()
	defer sess.closeWithError(io.EOF)

	payload, err := sess.call(context.Background(), mcptools.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      mcptools.RawStringID("2"),
		Method:  "unknown/method",
	})
	if err != nil {
		t.Fatalf("unexpected Go error (server errors should be in envelope): %v", err)
	}
	errField, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'error' field in envelope, got %v", payload)
	}
	if errField["code"] != int64(-32601) {
		t.Errorf("unexpected error code: %v", errField["code"])
	}
	if _, ok := payload["result"]; ok {
		t.Errorf("unexpected 'result' field in error envelope")
	}
}

// TestMCPSession_InitializeRetryAfterFailure tests that the session retries
// the initialize handshake after the first attempt fails.
func TestMCPSession_InitializeRetryAfterFailure(t *testing.T) {
	initCalls := 0
	conn := newFakeMCPConnection(func(req *sdkjsonrpc.Request) (*sdkjsonrpc.Response, error) {
		switch req.Method {
		case "initialize":
			initCalls++
			if initCalls == 1 {
				return &sdkjsonrpc.Response{
					ID:    req.ID,
					Error: &sdkjsonrpc.Error{Code: -32603, Message: "temporary init failure"},
				}, nil
			}
			return jsonRPCSuccessResponse(req.ID, map[string]any{"protocolVersion": "2025-06-18"}), nil
		case "tools/list":
			return jsonRPCSuccessResponse(req.ID, map[string]any{"tools": []any{}}), nil
		default:
			return nil, nil
		}
	})
	sess := newTestMCPSession(conn)
	go sess.readLoop()
	defer sess.closeWithError(io.EOF)

	_, firstErr := sess.call(context.Background(), mcptools.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      mcptools.RawStringID("1"),
		Method:  "tools/list",
	})
	if firstErr == nil {
		t.Fatal("first call should fail when initialize fails")
	}

	secondPayload, secondErr := sess.call(context.Background(), mcptools.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      mcptools.RawStringID("2"),
		Method:  "tools/list",
	})
	if secondErr != nil {
		t.Fatalf("second call should recover by retrying initialize: %v", secondErr)
	}
	if initCalls != 2 {
		t.Fatalf("initialize should be retried once, got calls: %d", initCalls)
	}
	result, ok := secondPayload["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing tools/list result in envelope: %#v", secondPayload)
	}
	if _, ok := result["tools"].([]any); !ok {
		t.Fatalf("missing tools field: %#v", result)
	}
}

// TestMCPSession_ExplicitInitializeNoDoubling tests that sending an explicit
// "initialize" call does not cause the session to auto-initialize again.
func TestMCPSession_ExplicitInitializeNoDoubling(t *testing.T) {
	initializeCalls := 0
	initializedNotifications := 0
	conn := newFakeMCPConnection(func(req *sdkjsonrpc.Request) (*sdkjsonrpc.Response, error) {
		switch req.Method {
		case "initialize":
			initializeCalls++
			return jsonRPCSuccessResponse(req.ID, map[string]any{"protocolVersion": "2025-06-18"}), nil
		case "notifications/initialized":
			initializedNotifications++
			return nil, nil
		case "tools/list":
			return jsonRPCSuccessResponse(req.ID, map[string]any{"tools": []any{}}), nil
		default:
			return nil, nil
		}
	})
	sess := newTestMCPSession(conn)
	go sess.readLoop()
	defer sess.closeWithError(io.EOF)

	_, initErr := sess.call(context.Background(), mcptools.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      mcptools.RawStringID("100"),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"v1"}}`),
	})
	if initErr != nil {
		t.Fatalf("explicit initialize should succeed: %v", initErr)
	}

	_, listErr := sess.call(context.Background(), mcptools.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      mcptools.RawStringID("101"),
		Method:  "tools/list",
	})
	if listErr != nil {
		t.Fatalf("tools/list after initialize should succeed: %v", listErr)
	}
	if initializeCalls != 1 {
		t.Fatalf("initialize should not be duplicated, got: %d", initializeCalls)
	}
	if initializedNotifications != 1 {
		t.Fatalf("should send exactly one notifications/initialized, got: %d", initializedNotifications)
	}
}

// TestMCPSession_PendingCleanupOnContextCancel tests that cancelling a request
// context removes it from the pending map.
func TestMCPSession_PendingCleanupOnContextCancel(t *testing.T) {
	conn := newFakeMCPConnection(func(_ *sdkjsonrpc.Request) (*sdkjsonrpc.Response, error) {
		// Never reply — caller should time out.
		return nil, nil
	})
	sess := newTestMCPSession(conn)
	sess.initState = mcpSessionInitStateReady

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := sess.call(ctx, mcptools.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      mcptools.RawStringID("200"),
		Method:  "tools/list",
	})
	if err == nil {
		t.Fatal("call should fail on context timeout")
	}

	sess.pendingMu.Lock()
	pendingCount := len(sess.pending)
	sess.pendingMu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("pending map should be empty after cancellation, got: %d", pendingCount)
	}
}

// TestMCPSession_PendingCleanupOnClose tests that closing the session drains
// all pending channels (callers unblock).
func TestMCPSession_PendingCleanupOnClose(t *testing.T) {
	conn := newFakeMCPConnection(func(_ *sdkjsonrpc.Request) (*sdkjsonrpc.Response, error) {
		return nil, nil // never reply
	})
	sess := newTestMCPSession(conn)
	sess.initState = mcpSessionInitStateReady

	errCh := make(chan error, 1)
	go func() {
		_, err := sess.call(context.Background(), mcptools.JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      mcptools.RawStringID("300"),
			Method:  "tools/list",
		})
		errCh <- err
	}()

	// Give goroutine time to register in pending.
	time.Sleep(10 * time.Millisecond)
	sess.closeWithError(io.EOF)

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("expected error after session close, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call did not unblock after session close")
	}

	sess.pendingMu.Lock()
	pendingCount := len(sess.pending)
	sess.pendingMu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("pending map should be empty after close, got: %d", pendingCount)
	}
}

// TestMCPSession_ReadLoopCancelOnClose tests that closing the session
// (which cancels readCtx) causes readLoop to exit.
func TestMCPSession_ReadLoopCancelOnClose(t *testing.T) {
	conn := newFakeMCPConnection(nil)
	sess := newTestMCPSession(conn)

	loopDone := make(chan struct{})
	go func() {
		sess.readLoop()
		close(loopDone)
	}()

	// Close the session; this should cancel readCtx and unblock readLoop.
	sess.closeWithError(io.EOF)

	select {
	case <-loopDone:
		// readLoop exited as expected.
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop did not exit after session close")
	}
}
