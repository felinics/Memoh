package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

func TestTerminalPingDoesNotWriteStdin(t *testing.T) {
	dialer := newScriptedDialer(func(stream *scriptedTerminalStream) {
		stream.enqueue(&pb.TerminalServer{Frame: &pb.TerminalServer_Ready{Ready: &pb.TerminalReady{
			SessionId: "sess",
			Offset:    0,
		}}})
		<-stream.ctx.Done()
	})
	conn := dialTerminal(t, dialer, 20*time.Millisecond, time.Second)
	if err := conn.WriteJSON(map[string]any{"type": "open", "cols": 80, "rows": 24}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	_ = conn.Close()

	frames := dialer.frames()
	for _, frame := range frames {
		if len(frame.GetInput()) > 0 {
			t.Fatalf("ping produced stdin %q", frame.GetInput())
		}
	}
	if len(frames) == 0 || frames[0].GetOpen() == nil {
		t.Fatalf("frames = %#v, want an open", frames)
	}
}

func TestTerminalExitUsesNormalClosure(t *testing.T) {
	dialer := newScriptedDialer(func(stream *scriptedTerminalStream) {
		stream.enqueue(&pb.TerminalServer{Frame: &pb.TerminalServer_Ready{Ready: &pb.TerminalReady{SessionId: "sess"}}})
		stream.enqueue(&pb.TerminalServer{Frame: &pb.TerminalServer_Exit{Exit: &pb.TerminalExit{Code: 3}}})
	})
	conn := dialTerminal(t, dialer)
	if err := conn.WriteJSON(map[string]any{"type": "open", "cols": 20, "rows": 10}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, ready, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ready), `"type":"ready"`) {
		t.Fatalf("ready = %s", ready)
	}
	_, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(message), `"type":"exit"`) || !strings.Contains(string(message), `"code":3`) {
		t.Fatalf("message = %s", message)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err = conn.ReadMessage()
	closeErr := &websocket.CloseError{}
	if !errorAsClose(err, closeErr) || closeErr.Code != websocket.CloseNormalClosure {
		t.Fatalf("close = %v", err)
	}
}

func TestTerminalReleaseIsForwardedOnce(t *testing.T) {
	dialer := newScriptedDialer(func(stream *scriptedTerminalStream) {
		stream.enqueue(&pb.TerminalServer{Frame: &pb.TerminalServer_Ready{Ready: &pb.TerminalReady{SessionId: "sess"}}})
		<-stream.ctx.Done()
	})
	conn := dialTerminal(t, dialer)
	if err := conn.WriteJSON(map[string]any{"type": "open", "cols": 20, "rows": 10}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, _ = conn.ReadMessage()
	if err := conn.WriteJSON(map[string]any{"type": "release"}); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	waitDialerQuiet(t, dialer)

	releases := 0
	for _, frame := range dialer.frames() {
		if frame.GetRelease() {
			releases++
		}
		if len(frame.GetInput()) > 0 {
			t.Fatalf("unexpected stdin %q", frame.GetInput())
		}
	}
	if releases == 0 {
		t.Fatal("release was not forwarded")
	}
}

func TestTerminalClientCloseDetachesWithoutRelease(t *testing.T) {
	dialer := newScriptedDialer(func(stream *scriptedTerminalStream) {
		stream.enqueue(&pb.TerminalServer{Frame: &pb.TerminalServer_Ready{Ready: &pb.TerminalReady{SessionId: "sess"}}})
		<-stream.ctx.Done()
	})
	conn := dialTerminal(t, dialer)
	if err := conn.WriteJSON(map[string]any{"type": "open", "cols": 20, "rows": 10}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, _ = conn.ReadMessage()
	_ = conn.Close()
	waitDialerQuiet(t, dialer)
	for _, frame := range dialer.frames() {
		if frame.GetRelease() {
			t.Fatal("client close forwarded release")
		}
	}
}

func TestTerminalMissingSessionClosesGone(t *testing.T) {
	dialer := &scriptedDialer{err: status.Error(codes.NotFound, "terminal session not found")}
	conn := dialTerminal(t, dialer)
	if err := conn.WriteJSON(map[string]any{"type": "attach", "sessionId": "missing", "since": 0, "cols": 20, "rows": 10}); err != nil {
		t.Fatal(err)
	}
	_, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(message), `"type":"gone"`) {
		t.Fatalf("message = %s", message)
	}
	_, _, err = conn.ReadMessage()
	closeErr := &websocket.CloseError{}
	if !errorAsClose(err, closeErr) || closeErr.Code != terminalCloseGone {
		t.Fatalf("close = %v", err)
	}
}

func TestTerminalUnimplementedDoesNotUseExec(t *testing.T) {
	dialer := &scriptedDialer{err: status.Error(codes.Unimplemented, "unknown method Terminal")}
	conn := dialTerminal(t, dialer)
	if err := conn.WriteJSON(map[string]any{"type": "open", "cols": 20, "rows": 10}); err != nil {
		t.Fatal(err)
	}
	_, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(message), `"type":"unsupported"`) {
		t.Fatalf("message = %s", message)
	}
	_, _, err = conn.ReadMessage()
	closeErr := &websocket.CloseError{}
	if !errorAsClose(err, closeErr) || closeErr.Code != terminalCloseUnsupported {
		t.Fatalf("close = %v", err)
	}
	if dialer.calls() != 1 {
		t.Fatalf("dials = %d, want 1", dialer.calls())
	}
}

func dialTerminal(t *testing.T, dialer terminalDialer, keepalive ...time.Duration) *websocket.Conn {
	t.Helper()
	pingInterval, pongWait := terminalPingInterval, terminalPongWait
	if len(keepalive) > 0 {
		pingInterval = keepalive[0]
	}
	if len(keepalive) > 1 {
		pongWait = keepalive[1]
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := terminalUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serveTerminalWebSocket(r.Context(), nil, "bot", conn, dialer, "sleep 30", t.TempDir(), pingInterval, pongWait)
	}))
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

type scriptedDialer struct {
	mu       sync.Mutex
	recorded []*pb.TerminalClient
	callN    int
	err      error
	handle   func(*scriptedTerminalStream)
}

func newScriptedDialer(handle func(*scriptedTerminalStream)) *scriptedDialer {
	return &scriptedDialer{handle: handle}
}

func (d *scriptedDialer) Terminal(ctx context.Context) (terminalRPC, error) {
	d.mu.Lock()
	d.callN++
	err := d.err
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	stream := newScriptedTerminalStream(ctx, d)
	if d.handle != nil {
		go d.handle(stream)
	}
	return stream, nil
}

func (d *scriptedDialer) record(msg *pb.TerminalClient) {
	if msg == nil {
		return
	}
	d.mu.Lock()
	d.recorded = append(d.recorded, msg)
	d.mu.Unlock()
}

func (d *scriptedDialer) frames() []*pb.TerminalClient {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*pb.TerminalClient, len(d.recorded))
	copy(out, d.recorded)
	return out
}

func (d *scriptedDialer) calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.callN
}

func waitDialerQuiet(t *testing.T, dialer *scriptedDialer) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var last int
	for time.Now().Before(deadline) {
		n := len(dialer.frames())
		if n == last && n > 0 {
			time.Sleep(30 * time.Millisecond)
			if len(dialer.frames()) == n {
				return
			}
		}
		last = n
		time.Sleep(20 * time.Millisecond)
	}
}

type scriptedTerminalStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	dial   *scriptedDialer
	recv   chan *pb.TerminalServer
}

func newScriptedTerminalStream(ctx context.Context, dial *scriptedDialer) *scriptedTerminalStream {
	ctx, cancel := context.WithCancel(ctx)
	return &scriptedTerminalStream{ctx: ctx, cancel: cancel, dial: dial, recv: make(chan *pb.TerminalServer, 4)}
}

func (s *scriptedTerminalStream) enqueue(msg *pb.TerminalServer) {
	select {
	case s.recv <- msg:
	case <-s.ctx.Done():
	}
}

func (s *scriptedTerminalStream) Send(msg *pb.TerminalClient) error {
	s.dial.record(msg)
	return nil
}

func (s *scriptedTerminalStream) Recv() (*pb.TerminalServer, error) {
	select {
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case msg, ok := <-s.recv:
		if !ok {
			return nil, s.ctx.Err()
		}
		return msg, nil
	}
}

func (s *scriptedTerminalStream) Close() error {
	s.cancel()
	return nil
}

func errorAsClose(err error, target *websocket.CloseError) bool {
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		return false
	}
	*target = *closeErr
	return true
}
